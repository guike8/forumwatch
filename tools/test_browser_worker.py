import json
from pathlib import Path
import subprocess
import sys
import tempfile
from types import SimpleNamespace
import unittest

from browser_worker import MAX_REQUEST, Worker, blocked_page, same_source


class FakePage:
    def __init__(self, url, body, engine=None):
        self.url = url
        self.body = body
        self.engine = engine
        self.main_frame = object()
        self.listeners = {}
        self.evaluations = []
        self.next_result = None

    def content(self):
        return self.body

    def is_closed(self):
        return False

    def on(self, event, callback):
        self.listeners[event] = callback

    def remove_listener(self, event, callback):
        self.listeners.pop(event, None)

    def goto(self, url, **kwargs):
        self.url = url
        self.engine.fetches += 1
        self.engine.navigation_options = kwargs
        response = SimpleNamespace(status=200, headers={},
                                   request=SimpleNamespace(is_navigation_request=lambda: True, frame=self.main_frame))
        if 'response' in self.listeners:
            self.listeners['response'](response)
        return response

    def evaluate(self, script, args):
        self.evaluations.append(args)
        return self.next_result or {'url': args['url'], 'status': 200, 'headers': {}, 'body': self.body}


class FakeEngine:
    def __init__(self, **kwargs):
        self.options = kwargs
        self.context = SimpleNamespace(route=lambda *args: None, new_page=self.new_page)
        self.fetches = 0
        self.closed = False
        self.page = None

    def start(self):
        pass

    def new_page(self):
        self.page = FakePage('about:blank', '<html><title>Forum</title><body>public post</body></html>', self)
        return self.page

    def close(self):
        self.closed = True


class BrowserWorkerTests(unittest.TestCase):
    def setUp(self):
        self.directory = tempfile.TemporaryDirectory()
        self.addCleanup(self.directory.cleanup)
        self.engines = []
        def factory(**kwargs):
            engine = FakeEngine(**kwargs)
            self.engines.append(engine)
            return engine
        self.worker = Worker(factory)
        self.addCleanup(self.worker.close)
        self.request = {'id': 1, 'url': 'http://forum.onion/threads/1/',
                        'proxy': 'socks5://127.0.0.1:9050',
                        'profile_dir': str(Path(self.directory.name) / 'profile'),
                        'timeout_ms': 40000, 'max_body_bytes': 2097152}

    def test_reuses_browser_and_uses_in_browser_fetch(self):
        first = self.worker.fetch(self.request)
        second = self.worker.fetch({**self.request, 'id': 2, 'url': 'http://forum.onion/threads/2/'})
        self.assertEqual((first['status'], second['status']), (200, 200))
        self.assertEqual(len(self.engines), 1)
        self.assertEqual(self.engines[0].fetches, 1)
        self.assertEqual(len(self.worker.page.evaluations), 1)
        options = self.engines[0].options
        self.assertEqual(options['proxy'], self.request['proxy'])
        self.assertFalse(options['google_search'])
        self.assertFalse(options['additional_args']['ignore_https_errors'])
        self.assertTrue(options['additional_args']['chromium_sandbox'])
        self.assertEqual(options['additional_args']['permissions'], [])
        self.assertEqual(Path(self.request['profile_dir']).stat().st_mode & 0o777, 0o700)

    def test_warm_fetch_uses_upgraded_https_origin(self):
        self.worker.fetch(self.request)
        self.worker.page.url = 'https://forum.onion/threads/1/'
        self.worker.page.body = '<title>Forum</title>'
        result = self.worker.fetch({**self.request, 'id': 2, 'url': 'http://forum.onion:80/threads/2/'})
        self.assertEqual(result['status'], 200)
        self.assertEqual(self.worker.page.evaluations[-1]['url'], 'https://forum.onion/threads/2/')

    def test_rejects_cross_source_or_proxy_changes(self):
        for change in ({'url': 'https://evil.invalid/'}, {'proxy': 'socks5://127.0.0.1:9150'}):
            self.worker.fetch(self.request)
            result = self.worker.fetch({**self.request, **change})
            self.assertEqual(result['error_kind'], 'configuration')
            self.assertTrue(self.engines[-1].closed)

    def test_rejects_cross_source_final_response(self):
        self.worker.fetch(self.request)
        self.worker.page.next_result = {'status': 200, 'url': 'http://clearnet.invalid/',
                                        'body': 'not onion content', 'headers': {}}
        result = self.worker.fetch({**self.request, 'id': 2})
        self.assertEqual(result['error_kind'], 'scope')
        self.assertTrue(self.engines[0].closed)

    def test_login_is_error_and_no_page_content_is_returned(self):
        self.worker.fetch(self.request)
        self.worker.page.body = '<title>Sign In</title><input type=password>'
        result = self.worker.fetch({**self.request, 'id': 2})
        self.assertEqual(result['error_kind'], 'authentication_required')
        self.assertNotIn('body', result)
        self.assertTrue(self.engines[0].closed)

    def test_preserves_http_error_and_retry_after(self):
        self.worker.fetch(self.request)
        self.worker.page.next_result = {'status': 429, 'url': self.request['url'], 'body': 'slow down',
                                        'headers': {'retry-after': '120'}}
        result = self.worker.fetch({**self.request, 'id': 2})
        self.assertEqual(result['status'], 429)
        self.assertEqual(result['headers']['retry-after'], '120')

    def test_response_limit_and_sanitized_failure(self):
        self.worker.fetch(self.request)
        result = self.worker.fetch({**self.request, 'max_body_bytes': 4})
        self.assertEqual(result['error_kind'], 'body_too_large')
        self.assertNotIn('body', result)
        self.assertNotIn('forum.onion', result['error'])

    def test_rejects_unsupported_proxy_credentials_and_profile_symlinks(self):
        for proxy in ('http://127.0.0.1:9050', 'socks5://user:secret@127.0.0.1:9050'):
            result = self.worker.fetch({**self.request, 'proxy': proxy})
            self.assertEqual(result['error_kind'], 'configuration')
        link = Path(self.directory.name) / 'link'
        link.symlink_to(self.directory.name, target_is_directory=True)
        result = self.worker.fetch({**self.request, 'profile_dir': str(link)})
        self.assertEqual(result['error_kind'], 'configuration')
        self.assertFalse(self.engines)

    def test_protocol_stops_on_oversized_request_without_launching_browser(self):
        process = subprocess.run([sys.executable, '-I', str(Path(__file__).with_name('browser_worker.py'))],
                                 input=b' ' * (MAX_REQUEST + 1) + b'\n', capture_output=True, timeout=5)
        self.assertEqual(process.returncode, 0)
        self.assertEqual(process.stdout, b'')
        self.assertEqual(process.stderr, b'')

    def test_source_boundaries(self):
        self.assertTrue(same_source('http://forum.onion/', 'https://forum.onion/posts/1'))
        self.assertTrue(same_source('http://forum.onion/', 'http://FORUM.onion.:80/'))
        self.assertFalse(same_source('https://forum.onion/', 'http://forum.onion/'))
        self.assertFalse(same_source('http://forum.onion/', 'http://forum.onion:8080/'))
        self.assertFalse(same_source('http://forum.onion/', 'http://sub.forum.onion/'))

    def test_page_classification(self):
        self.assertEqual(blocked_page('http://forum.onion/', '<title>Browser check…</title>'), 'challenge')
        self.assertEqual(blocked_page('http://forum.onion/challenge/?return=/', '<title>Wait</title>'), 'challenge')
        self.assertEqual(blocked_page('http://forum.onion/login/', '<title>Forum</title>'), 'authentication_required')
        self.assertIsNone(blocked_page('http://forum.onion/threads/1/', '<title>Discussion</title><p>We discussed a captcha.</p>'))
        self.assertIsNone(blocked_page('http://forum.onion/threads/1/', '<title>Research: CAPTCHA and browser checks</title>'))


if __name__ == '__main__':
    unittest.main()
