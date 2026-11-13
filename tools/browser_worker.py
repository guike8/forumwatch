#!/usr/bin/env python3
"""Persistent, source-scoped Scrapling worker. Stdin/stdout are private JSON lines."""
import ipaddress
import json
import logging
import os
from pathlib import Path
import sys
import time
from html.parser import HTMLParser
from urllib.parse import urlsplit

MAX_BODY = 2 << 20
MAX_REQUEST = 16384
logging.disable(logging.CRITICAL)


class CollectionError(Exception):
    def __init__(self, kind, message):
        self.kind = kind
        super().__init__(message)


def web_address(value):
    try:
        parsed = urlsplit(value)
        if parsed.scheme not in ('http', 'https') or not parsed.hostname or parsed.username is not None or parsed.password is not None:
            raise ValueError()
        host = parsed.hostname.rstrip('.').encode('idna').decode('ascii').lower()
        port = parsed.port or (443 if parsed.scheme == 'https' else 80)
        return parsed.scheme, host, port
    except (ValueError, UnicodeError, AttributeError):
        raise CollectionError('configuration', 'invalid HTTP(S) source URL') from None


def same_source(first, second):
    a, b = web_address(first), web_address(second)
    return a == b or (a[1] == b[1] and a[0] == 'http' and a[2] == 80 and b[0] == 'https' and b[2] == 443)


class PageFacts(HTMLParser):
    def __init__(self):
        super().__init__()
        self.title_parts = []
        self.title_depth = 0
        self.password = False

    def handle_starttag(self, tag, attrs):
        attrs = dict(attrs)
        if tag == 'title':
            self.title_depth += 1
        if tag == 'input' and attrs.get('type', '').lower() == 'password':
            self.password = True

    def handle_endtag(self, tag):
        if tag == 'title':
            self.title_depth = max(0, self.title_depth - 1)

    def handle_data(self, value):
        if self.title_depth:
            self.title_parts.append(value)


def blocked_page(url, body):
    facts = PageFacts()
    facts.feed(body)
    title = ' '.join(facts.title_parts).strip().lower()
    path = urlsplit(url).path.lower().rstrip('/')
    if facts.password or path in ('/login', '/register', '/signin'):
        return 'authentication_required'
    challenge_titles = {'browser check', 'just a moment', 'checking your browser', 'security check', 'captcha'}
    if path.startswith('/challenge') or title.strip(' .…!') in challenge_titles:
        return 'challenge'
    return None


# Runs inside the already-proxied browser; never a separate direct HTTP client.
FETCH = r"""async ({url, timeout, limit}) => {
    const controller = new AbortController();
    const timer = setTimeout(() => controller.abort(), timeout);
    try {
        const response = await fetch(url, {credentials: 'same-origin', cache: 'no-cache',
                                           redirect: 'follow', signal: controller.signal});
        const reader = response.body ? response.body.getReader() : null;
        const chunks = [];
        let size = 0;
        if (reader) {
            while (true) {
                const {done, value} = await reader.read();
                if (done) break;
                size += value.byteLength;
                if (size > limit) { await reader.cancel(); throw new Error('BODY_LIMIT'); }
                chunks.push(value);
            }
        }
        const bytes = new Uint8Array(size);
        let offset = 0;
        for (const chunk of chunks) { bytes.set(chunk, offset); offset += chunk.byteLength; }
        const type = response.headers.get('content-type') || '';
        const charset = /charset\s*=\s*["']?([^;\s"']+)/i.exec(type);
        const body = new TextDecoder(charset ? charset[1] : 'utf-8').decode(bytes);
        return {status: response.status, url: response.url, body,
                headers: {'content-type': type, 'retry-after': response.headers.get('retry-after') || ''}};
    } finally { clearTimeout(timer); }
}"""


class Worker:
    def __init__(self, engine_factory=None):
        self.engine_factory = engine_factory
        self.session = None
        self.page = None
        self.scope = None
        self.proxy = None
        self.profile = None
        self.deadline = 0
        self.max_body = MAX_BODY

    def remaining(self):
        milliseconds = int((self.deadline - time.monotonic()) * 1000)
        if milliseconds <= 0:
            raise CollectionError('timeout', 'browser request deadline exceeded')
        return milliseconds

    def configure(self, request):
        url = request['url']
        web_address(url)
        proxy = request['proxy']
        try:
            parsed = urlsplit(proxy)
            if parsed.scheme != 'socks5' or not parsed.hostname or not parsed.port or parsed.username is not None or parsed.password is not None:
                raise ValueError()
            proxy_host = parsed.hostname
        except (ValueError, TypeError):
            raise CollectionError('configuration', 'a SOCKS5 proxy without credentials is required') from None
        profile = Path(request['profile_dir'])
        if not profile.is_absolute() or profile.is_symlink():
            raise CollectionError('configuration', 'profile must be an absolute non-symlink path')
        timeout = request['timeout_ms']
        limit = request['max_body_bytes']
        if type(timeout) is not int or not 1 <= timeout <= 40000 or type(limit) is not int or not 1 <= limit <= MAX_BODY:
            raise CollectionError('configuration', 'invalid timeout or response limit')
        self.deadline = time.monotonic() + timeout / 1000
        self.max_body = limit
        if self.scope is not None:
            if not same_source(self.scope, url) or proxy != self.proxy or str(profile) != self.profile:
                raise CollectionError('configuration', 'worker cannot change source, proxy, or profile')
            return
        profile.mkdir(parents=True, mode=0o700, exist_ok=True)
        profile.chmod(0o700)
        self.scope, self.proxy, self.profile = url, proxy, str(profile)
        # Numeric proxy addresses can be excluded without permitting destination DNS.
        # Hostname proxies are left to Chromium's proxy resolver; no direct fallback.
        try:
            ipaddress.ip_address(proxy_host)
            exclude = proxy_host
        except ValueError:
            if any(c in proxy_host for c in ', *~'):
                raise CollectionError('configuration', 'invalid proxy hostname')
            exclude = proxy_host
        if self.engine_factory is None:
            from scrapling.fetchers import StealthySession
            self.engine_factory = StealthySession
        self.session = self.engine_factory(
            headless=True, proxy=proxy, user_data_dir=str(profile), retries=1,
            timeout=min(35000, self.remaining()), google_search=False,
            block_webrtc=True, dns_over_https=False, network_idle=False,
            solve_cloudflare=False,
            additional_args={'ignore_https_errors': False, 'permissions': [],
                             'accept_downloads': False, 'service_workers': 'block',
                             'chromium_sandbox': True},
            extra_flags=['--disable-quic', '--disable-extensions',
                         f'--host-resolver-rules=MAP * ~NOTFOUND, EXCLUDE localhost, EXCLUDE {exclude}',
                         '--proxy-bypass-list=<-loopback>'],
        )
        self.session.start()
        self.session.context.route('**/*', self.route)

    def route(self, route):
        request = route.request
        try:
            allowed = same_source(self.scope, request.url)
        except CollectionError:
            allowed = False
        if not allowed or request.resource_type in ('media', 'websocket', 'font') or '/attachments/' in urlsplit(request.url).path:
            route.abort()
        else:
            route.continue_()

    def render(self, url):
        if self.page is None or self.page.is_closed():
            self.page = self.session.context.new_page()
            self.page.on('download', lambda download: download.cancel())
        page = self.page
        document = [None]
        def received(response):
            if response.request.is_navigation_request() and response.request.frame == page.main_frame:
                document[0] = response
        page.on('response', received)
        try:
            # DOM readiness is enough for server-rendered posts. Waiting for every
            # image/ad to load can stall an otherwise usable Tor document.
            response = page.goto(url, wait_until='domcontentloaded', timeout=min(35000, self.remaining()))
            challenge_deadline = min(self.deadline, time.monotonic() + 12)
            while True:
                try:
                    content = page.content()
                    kind = blocked_page(page.url, content)
                except Exception:
                    if page.is_closed() or time.monotonic() >= challenge_deadline:
                        raise
                    page.wait_for_timeout(min(250, self.remaining()))
                    continue
                if kind != 'challenge' or time.monotonic() >= challenge_deadline:
                    break
                page.wait_for_timeout(min(250, self.remaining()))
            final = document[0] or response
            if final is None:
                raise CollectionError('browser_error', 'browser did not capture a document response')
            headers = final.headers
            return {'body': content, 'url': page.url, 'status': final.status,
                    'headers': {'content-type': headers.get('content-type', ''),
                                'retry-after': headers.get('retry-after', '')}}
        finally:
            page.remove_listener('response', received)

    def fetch(self, request):
        try:
            self.configure(request)
            url = request['url']
            if self.page is None or self.page.is_closed():
                result = self.render(url)
            else:
                # An HTTP source may have upgraded to HTTPS during navigation.
                # Fetch directly at that origin to avoid mixed-content blocking.
                target = urlsplit(url)
                current = urlsplit(self.page.url)
                if (target.scheme == 'http' and current.scheme == 'https'
                        and same_source(url, self.page.url)):
                    url = target._replace(scheme='https', netloc=current.netloc).geturl()
                try:
                    result = self.page.evaluate(FETCH, {'url': url, 'timeout': self.remaining(), 'limit': self.max_body})
                except Exception as error:
                    if 'BODY_LIMIT' in str(error):
                        raise CollectionError('body_too_large', 'browser response exceeds configured limit') from None
                    raise CollectionError('browser_error', 'browser fetch failed; session will be restarted') from None
                if not same_source(self.scope, result['url']):
                    raise CollectionError('scope', 'browser response left the configured source')
                if blocked_page(result['url'], result['body']) == 'challenge':
                    result = self.render(url)
            if not same_source(self.scope, result['url']):
                raise CollectionError('scope', 'browser response left the configured source')
            if len(result['body'].encode('utf-8')) > self.max_body:
                raise CollectionError('body_too_large', 'browser response exceeds configured limit')
            if not isinstance(result['status'], int) or not 100 <= result['status'] <= 599:
                raise CollectionError('browser_error', 'browser returned an invalid HTTP status')
            # Preserve HTTP errors, especially Retry-After, for the Go scheduler.
            if result['status'] == 200:
                kind = blocked_page(result['url'], result['body'])
                if kind:
                    message = 'source requires authentication' if kind == 'authentication_required' else 'source is still serving a browser challenge'
                    raise CollectionError(kind, message)
            self.remaining()
            return {'id': request['id'], **result}
        except CollectionError as error:
            self.close()
            return {'id': request.get('id', 0), 'error_kind': error.kind, 'error': str(error)}
        except Exception as error:
            self.close()
            if type(error).__name__ == 'TimeoutError':
                return {'id': request.get('id', 0), 'error_kind': 'timeout',
                        'error': 'browser request timed out'}
            return {'id': request.get('id', 0), 'error_kind': 'browser_error',
                    'error': 'browser acquisition failed; check source availability and Tor'}

    def close(self):
        if self.session is not None:
            try:
                self.session.close()
            except Exception:
                pass
        self.session, self.page = None, None
        self.scope = self.proxy = self.profile = None


def main():
    os.umask(0o077)
    worker = Worker()
    try:
        while True:
            line = sys.stdin.buffer.readline(MAX_REQUEST + 1)
            if not line:
                return
            if len(line) > MAX_REQUEST:
                return
            try:
                request = json.loads(line)
                if not isinstance(request, dict) or type(request.get('id')) is not int or request['id'] < 1:
                    return
                response = worker.fetch(request)
            except (ValueError, UnicodeError):
                return
            sys.stdout.write(json.dumps(response, ensure_ascii=True) + '\n')
            sys.stdout.flush()
    finally:
        worker.close()


if __name__ == '__main__':
    main()
