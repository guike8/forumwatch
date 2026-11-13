import SwiftUI
import AppKit

@MainActor
final class AppDelegate: NSObject, NSApplicationDelegate {
    weak var model: AppModel?
    private var quitting = false
    func applicationShouldTerminate(_ sender: NSApplication) -> NSApplication.TerminateReply {
        guard let model, model.phase != .stopped else { return .terminateNow }
        guard !quitting else { return .terminateLater }
        quitting = true
        Task {
            await model.stop()
            sender.reply(toApplicationShouldTerminate: true)
        }
        return .terminateLater
    }
    func applicationShouldTerminateAfterLastWindowClosed(_ sender: NSApplication) -> Bool { false }
}

@main
struct NightwireApp: App {
    @NSApplicationDelegateAdaptor(AppDelegate.self) private var delegate
    @StateObject private var model: AppModel
    @State private var smokeLaunched = false
    private let smokeRoot: URL?
    init() {
        do {
            let options = try LaunchOptions(arguments: CommandLine.arguments)
            smokeRoot = options.smoke ? options.root : nil
            _model = StateObject(wrappedValue: AppModel(storageRoot: options.root, requireEmptyRoot: options.smoke))
        } catch {
            smokeRoot = nil
            _model = StateObject(wrappedValue: AppModel(startupError: error.localizedDescription))
        }
    }
    var body: some Scene {
        Window("Nightwire", id: "main") {
            ContentView(model: model)
                .onAppear {
                    delegate.model = model
                    if let smokeRoot, !smokeLaunched {
                        smokeLaunched = true
                        Task { await model.runSmoke(resultDirectory: smokeRoot); NSApplication.shared.terminate(nil) }
                    }
                }
                .frame(minWidth: 600, minHeight: 500)
        }
        .defaultSize(width: 1250, height: 780)
        .commands {
            CommandGroup(replacing: .newItem) {
                Button("Add Source…") { NotificationCenter.default.post(name: .nightwireAddSource, object: nil) }
                    .keyboardShortcut("n", modifiers: .command)
                    .disabled(model.configuration == nil || model.phase == .stopping)
            }
            CommandMenu("Monitoring") {
                Button("Filters…") { NotificationCenter.default.post(name: .nightwireFilters, object: nil) }
                    .keyboardShortcut("f", modifiers: [.command, .shift])
                    .disabled(model.sources.isEmpty || model.phase == .starting || model.phase == .stopping)
                Divider()
                Button(model.phase == .running || model.phase == .starting ? "Stop" : "Start") {
                    Task {
                        if model.phase == .running || model.phase == .starting { await model.stop() }
                        else { await model.start() }
                    }
                }.keyboardShortcut("r", modifiers: .command)
                    .disabled(model.configuration == nil || model.phase == .stopping)
            }
        }
        MenuBarExtra {
            NightwireMenu(model: model)
        } label: {
            Image(nsImage: Branding.menuBarIcon)
                .renderingMode(.template)
                .accessibilityLabel("Nightwire")
        }
        .menuBarExtraStyle(.menu)
    }
}

extension Notification.Name {
    static let nightwireAddSource = Notification.Name("NightwireAddSource")
    static let nightwireFilters = Notification.Name("NightwireFilters")
    static let nightwireDismissEditor = Notification.Name("NightwireDismissEditor")
}

private struct EditorSelection: Identifiable {
    let id = UUID()
    let source: Source?
}

struct ContentView: View {
    @ObservedObject var model: AppModel
    @State private var sourceID: String? = "all"
    @State private var itemID: String?
    @State private var search = ""
    @State private var editor: EditorSelection?
    @State private var filtersPresented = false
    @State private var deleting: Source?
    @State private var columnVisibility: NavigationSplitViewVisibility = .all
    @State private var compactLayout = false
    private var selectedSource: Source? { model.sources.first { $0.id == sourceID } }
    private var selectedItem: Item? { model.items.first { $0.id == itemID } }
    private var visibleItems: [Item] {
        model.items.filter {
            (sourceID == "all" || sourceID == nil || $0.source == sourceID) &&
            (search.isEmpty || ($0.title + " " + ($0.body ?? $0.summary ?? "")).localizedCaseInsensitiveContains(search))
        }
    }
    private var busy: Bool { model.phase == .starting || model.phase == .stopping }
    var body: some View {
        GeometryReader { geometry in
            splitView
                .onChange(of: geometry.size.width, initial: true) { _, width in
                    let compact = width < 1000
                    if compact != compactLayout {
                        compactLayout = compact
                        columnVisibility = compact ? .doubleColumn : .all
                    }
                }
        }
    }
    private var splitView: some View {
        NavigationSplitView(columnVisibility: $columnVisibility) {
            VStack(spacing: 0) {
                List(selection: $sourceID) {
                    Label("All sources", systemImage: "tray.full").tag("all")
                    Section("Sources") {
                        ForEach(model.sources) { source in
                            VStack(alignment: .leading, spacing: 4) {
                                Text(verbatim: source.displayName).lineLimit(1)
                                Text(verbatim: source.format + " · Tor / HTTP")
                                    .font(.caption).foregroundStyle(.secondary)
                            }
                            .tag(source.id)
                            .contextMenu {
                                Button("Edit source…") { editor = EditorSelection(source: source) }
                                Button("Filters…") { sourceID = source.id; filtersPresented = true }
                                    .disabled(busy)
                                Button("Copy source link") { copyText(source.url) }
                                Button("Delete source…", role: .destructive) { deleting = source }
                            }
                        }
                    }
                }
                Divider()
                VStack(alignment: .leading, spacing: 8) {
                    HStack {
                        Button { editor = EditorSelection(source: nil) } label: { Image(systemName: "plus") }
                            .help("Add source").accessibilityLabel("Add source")
                        if let selectedSource {
                            Button { editor = EditorSelection(source: selectedSource) } label: { Image(systemName: "pencil") }
                                .help("Edit source").accessibilityLabel("Edit source")
                        }
                        Spacer()
                    }.buttonStyle(.borderless).disabled(busy)
                    Text(model.sources.isEmpty ? "No sources configured · no monitoring" : model.phase.rawValue)
                        .font(.caption).foregroundStyle(.secondary)
                    Text("Tor only · no direct fallback").font(.caption2).foregroundStyle(.secondary)
                }.padding(12)
            }
            .navigationTitle("Nightwire")
            .navigationSplitViewColumnWidth(min: 180, ideal: 220, max: 280)
        } content: {
            VStack(spacing: 0) {
                if let source = selectedSource {
                    SourceHealth(source: source, status: model.statuses.first { $0.id == source.id }, phase: model.phase)
                    Divider()
                }
                if visibleItems.isEmpty {
                    ContentUnavailableView {
                        Label("No items", systemImage: "text.bubble")
                    } description: {
                        Text(model.sources.isEmpty ? "Choose a starter from the source catalog or add your own. Nothing is monitored by default." : "Start the backend and wait for the first Tor poll. Initial posts are baseline, not new alerts.")
                    } actions: {
                        if model.sources.isEmpty {
                            Button("Browse source catalog…") { editor = EditorSelection(source: nil) }
                                .disabled(busy || model.configuration == nil)
                        }
                    }
                } else {
                    List(visibleItems, selection: $itemID) { item in
                        ItemRow(item: item).tag(item.id)
                    }.listStyle(.inset)
                }
                Divider()
                HStack {
                    VStack(alignment: .leading, spacing: 3) {
                        Text(verbatim: model.connection).font(.caption).foregroundStyle(.secondary).lineLimit(2)
                        if let notice = model.statusNotice {
                            Text(verbatim: notice).font(.caption).foregroundStyle(.secondary).lineLimit(2)
                        }
                    }
                    Spacer()
                    if model.phase == .running {
                        Button("Reconnect") { model.reconnect() }.font(.caption)
                    }
                }.padding(10)
            }
            .navigationTitle(selectedSource?.displayName ?? "Live items")
            .navigationSplitViewColumnWidth(min: 240, ideal: 320, max: 420)
            .searchable(text: $search, prompt: "Search received text")
        } detail: {
            if let selectedItem { ItemDetail(item: selectedItem) }
            else { ContentUnavailableView("Select an item", systemImage: "doc.text", description: Text("Source claims are unverified. Posts are displayed as plain text.")) }
        }
        .navigationSplitViewStyle(.balanced)
        .toolbar(removing: .sidebarToggle)
        .toolbar {
            ToolbarItem(placement: .navigation) {
                if compactLayout {
                    Menu {
                        Button("All sources") { sourceID = "all" }
                        ForEach(model.sources) { source in
                            Button(source.displayName) { sourceID = source.id }
                        }
                    } label: {
                        Label("Choose source", systemImage: "tray.full")
                    }
                    .help("Choose a source")
                } else {
                    Button {
                        columnVisibility = columnVisibility == .all ? .doubleColumn : .all
                    } label: {
                        Label("Toggle sources sidebar", systemImage: "sidebar.left")
                    }
                    .help("Toggle sources sidebar")
                }
            }
            ToolbarItem(placement: .primaryAction) {
                if model.phase == .running || model.phase == .starting {
                    Button { Task { await model.stop() } } label: { Label("Stop", systemImage: "stop.fill") }
                } else {
                    Button { Task { await model.start() } } label: { Label(model.phase == .stopping ? "Stopping…" : "Start", systemImage: "play.fill") }
                        .disabled(model.phase == .stopping || model.configuration == nil)
                }
            }
            ToolbarItem {
                Button { filtersPresented = true } label: {
                    Label("Filters", systemImage: model.sources.contains(where: { $0.alertFilter.isActive }) ? "line.3.horizontal.decrease.circle.fill" : "line.3.horizontal.decrease.circle")
                }
                .accessibilityLabel("Filters")
                .help("Filter settings (⇧⌘F)")
                .disabled(busy || model.sources.isEmpty)
            }
            ToolbarItem {
                Button { editor = EditorSelection(source: nil) } label: { Label("Add source", systemImage: "plus") }
                    .disabled(busy || model.configuration == nil)
            }
        }
        .sheet(item: $editor) { selection in
            SourceEditor(model: model, original: selection.source)
        }
        .sheet(isPresented: $filtersPresented) {
            FilterSettings(model: model, sourceID: sourceID)
        }
        .alert("Nightwire", isPresented: Binding(get: { model.error != nil }, set: { if !$0 { model.error = nil } })) {
            Button("OK") { model.error = nil }
        } message: { Text(verbatim: model.error ?? "") }
        .confirmationDialog("Delete source? Existing local state is retained, but this source will no longer be monitored.", isPresented: Binding(get: { deleting != nil }, set: { if !$0 { deleting = nil } })) {
            if let deleting {
                Button("Delete source", role: .destructive) {
                    Task { await model.delete(deleting); sourceID = "all"; self.deleting = nil }
                }
            }
            Button("Cancel", role: .cancel) { deleting = nil }
        }
        .onReceive(NotificationCenter.default.publisher(for: .nightwireAddSource)) { _ in
            guard !filtersPresented else { return }
            editor = EditorSelection(source: nil)
        }
        .onReceive(NotificationCenter.default.publisher(for: .nightwireFilters)) { _ in
            guard editor == nil else { return }
            filtersPresented = true
        }
        .onReceive(NotificationCenter.default.publisher(for: .nightwireDismissEditor)) { _ in
            editor = nil
            filtersPresented = false
        }
        .onChange(of: sourceID) { _, _ in itemID = nil }
        .onChange(of: model.sources.map(\.id)) { _, ids in
            if let sourceID, sourceID != "all", !ids.contains(sourceID) { self.sourceID = "all" }
        }
    }
}

private struct ItemRow: View {
    let item: Item
    var body: some View {
        VStack(alignment: .leading, spacing: 6) {
            Text(verbatim: item.title.isEmpty ? "Untitled post" : item.title).font(.headline).lineLimit(2)
            Text(verbatim: item.body ?? item.summary ?? "No readable text").font(.callout).foregroundStyle(.secondary).lineLimit(2)
            HStack(spacing: 8) {
                Text(verbatim: item.kind == "baseline" ? "Baseline" : item.kind.capitalized)
                Text(verbatim: accessLabel(item))
                Spacer()
                Text(item.observedDate, style: .time)
            }.font(.caption).foregroundStyle(.secondary)
        }.padding(.vertical, 6)
    }
}

private struct SourceHealth: View {
    let source: Source
    let status: SourceStatus?
    let phase: AppModel.Phase
    var body: some View {
        VStack(alignment: .leading, spacing: 6) {
            HStack {
                Text("Source status").font(.headline)
                Spacer()
                Button("Copy source link") { copyText(source.url) }.font(.caption)
            }
            Text(verbatim: source.url).font(.caption).foregroundStyle(.secondary).lineLimit(2).textSelection(.enabled)
            if let status {
                if let error = status.error, !error.isEmpty {
                    Text(verbatim: error).font(.caption).foregroundStyle(.red).textSelection(.enabled)
                } else if let date = Item.parseDate(status.lastSuccess), date > .distantPast, !status.lastSuccess.hasPrefix("0001") {
                    Text("Last successful poll: \(date.formatted(date: .abbreviated, time: .standard))").font(.caption)
                } else { Text("Waiting for the first successful poll").font(.caption).foregroundStyle(.secondary) }
                if source.format.hasPrefix("xenforo") || source.format.hasPrefix("mybb") {
                    Text("Crawl backlog: \(status.pendingPages ?? 0) pending pages · \(status.trackedThreads ?? 0) tracked threads · \(status.failedPages ?? 0) failed pages")
                        .font(.caption).foregroundStyle(.secondary)
                }
                if let matching = status.matchingItems, let filtered = status.filteredItems {
                    Text("Saved latest revisions: \(matching) matching, \(filtered) filtered · not delivered alert counts")
                        .font(.caption).foregroundStyle(.secondary)
                }
                if let next = Item.parseDate(status.nextCheck), !status.nextCheck.hasPrefix("0001") {
                    Text("Next poll: \(next.formatted(date: .omitted, time: .standard))").font(.caption).foregroundStyle(.secondary)
                }
            } else { Text(phase == .running ? "Waiting for source status" : "Backend is not monitoring").font(.caption).foregroundStyle(.secondary) }
        }.padding(12)
    }
}

private struct ItemDetail: View {
    let item: Item
    var body: some View {
        ScrollView {
            VStack(alignment: .leading, spacing: 20) {
                VStack(alignment: .leading, spacing: 10) {
                    Text(verbatim: item.title).font(.title2).fontWeight(.semibold)
                    Text("Unverified source claims").font(.subheadline).foregroundStyle(.orange)
                    HStack {
                        Text(verbatim: item.kind == "baseline" ? "Baseline · not a new alert" : item.kind.capitalized)
                        Text("·")
                        Text(verbatim: accessLabel(item))
                    }.font(.caption).foregroundStyle(.secondary)
                    if let author = item.author, !author.isEmpty { Text(verbatim: "Author: " + author).font(.callout) }
                    Text("Observed \(item.observedDate.formatted(date: .abbreviated, time: .standard))")
                        .font(.caption).foregroundStyle(.secondary)
                    if let count = item.hiddenLinks, count > 0 {
                        Text("\(count) hidden link(s) · access restrictions are not bypassed")
                            .font(.caption).foregroundStyle(.secondary)
                    }
                }
                Divider()
                if let extract = item.localExtract {
                    VStack(alignment: .leading, spacing: 8) {
                        Text("Local extract · not AI · unverified").font(.headline)
                        Text(verbatim: extract).font(.body)
                        Text("Extracted locally from visible source text; not an independent finding.")
                            .font(.caption).foregroundStyle(.secondary)
                    }
                    Divider()
                }
                Text("Post text").font(.headline)
                Text(verbatim: item.body ?? item.summary ?? "No readable post text is available.")
                    .font(.body).frame(maxWidth: .infinity, alignment: .leading)
                Divider()
                VStack(alignment: .leading, spacing: 8) {
                    Text("Source link").font(.headline)
                    Text(verbatim: item.url).font(.system(.caption, design: .monospaced))
                    Button("Copy source link") { copyText(item.url) }
                    Text("Links are never opened in your default browser. Use a Tor-aware browser separately.")
                        .font(.caption).foregroundStyle(.secondary)
                    Text(verbatim: "Source ID: " + item.source).font(.caption2).foregroundStyle(.secondary)
                }
            }.textSelection(.enabled).padding(24).frame(maxWidth: 780, alignment: .leading)
        }
    }
}

private func accessLabel(_ item: Item) -> String {
    switch item.access {
    case "restricted": return "Access: restricted"
    case "partial": return "Access: partial"
    case "public", "public-text": return "Access: public text"
    default: return (item.body?.isEmpty == false) ? "Access: visible text" : "Access: not assessed"
    }
}

private func copyText(_ text: String) {
    NSPasteboard.general.clearContents()
    NSPasteboard.general.setString(text, forType: .string)
}

private struct SourceEditor: View {
    @ObservedObject var model: AppModel
    let original: Source?
    @Environment(\.dismiss) private var dismiss
    @State private var draft: Source
    @State private var proxy: String
    @State private var saving = false
    @State private var error: String?
    @State private var catalog: [CatalogSource] = []
    @State private var catalogError: String?
    @State private var presetID = ""
    private var preset: CatalogSource? { catalog.first { $0.id == presetID } }
    init(model: AppModel, original: Source?) {
        self.model = model
        self.original = original
        _draft = State(initialValue: original ?? Source())
        _proxy = State(initialValue: model.configuration?.torProxy ?? "127.0.0.1:9050")
    }
    private var changesBinding: Bool { original.map { $0.url != draft.url || $0.format != draft.format } ?? false }
    var body: some View {
        VStack(alignment: .leading, spacing: 0) {
            Text(original == nil ? "Add source" : "Edit source").font(.title2).padding(20)
            Form {
                if original == nil {
                    Section("Source catalog") {
                        Picker("Starter source", selection: $presetID) {
                            Text("Custom source").tag("")
                            ForEach(catalog.filter { !$0.isConfigured(in: model.sources) }) { entry in
                                Text(verbatim: entry.name).tag(entry.id)
                            }
                        }
                        .accessibilityIdentifier("source.catalog")
                        .onChange(of: presetID) { _, id in
                            draft = catalog.first { $0.id == id }?.makeSource() ?? Source()
                        }
                        if let preset {
                            Text(verbatim: preset.category + " · " + preset.evidence)
                                .font(.caption).foregroundStyle(.secondary)
                            Text(verbatim: preset.notes).font(.caption).foregroundStyle(.secondary)
                        }
                        if let catalogError { Text(verbatim: catalogError).font(.caption).foregroundStyle(.orange) }
                        Text("Presets only fill this draft. Already-added presets are omitted. Review the URL and settings, then Save to add one. Availability can change; no login or access bypass is included. Public-web sources also use Tor.")
                            .font(.caption).foregroundStyle(.secondary)
                    }
                }
                Section("Source") {
                    TextField("URL", text: $draft.url)
                    Picker("Format", selection: $draft.format) {
                        ForEach(Source.formats, id: \.self) { Text(verbatim: $0).tag($0) }
                    }
                    TextField("Poll interval (seconds)", value: $draft.intervalSeconds, format: .number)
                    TextField("Include URL (Go regex)", text: $draft.includeURL)
                    Text("10–86400 seconds. HTML requires an include URL regex to select post links.")
                        .font(.caption).foregroundStyle(.secondary)
                    if changesBinding {
                        Text("Changing URL or format creates a new source ID and a fresh baseline. Existing crawl state is not reused.")
                            .font(.caption).foregroundStyle(.orange)
                    }
                }
                Section("Transport") {
                    LabeledContent("Route", value: "Tor only · HTTP fetcher")
                    TextField("Local Tor SOCKS address", text: $proxy)
                    Text("Default 127.0.0.1:9050. Tor must already be running. No direct fallback, browser automation, or Tor service is launched.")
                        .font(.caption).foregroundStyle(.secondary)
                }
                Section("Alert filters") {
                    AlertFilterControls(filter: $draft.alertFilter)
                }
                if draft.format.hasPrefix("xenforo") || draft.format.hasPrefix("mybb") {
                    Section("Bounded crawling") {
                        Stepper("Pages per poll: \(draft.maxPagesPerPoll)", value: $draft.maxPagesPerPoll, in: 1...32)
                        Stepper("Tracked threads: \(draft.maxThreads)", value: $draft.maxThreads, in: 1...4096)
                        Stepper("Request gap: \(draft.requestGapSeconds) seconds", value: $draft.requestGapSeconds, in: 1...60)
                    }
                }
                Section {
                    Text("Saved privately in \(model.storagePath). Running monitoring restarts after a save, only after the owned backend terminates.")
                        .font(.caption).foregroundStyle(.secondary)
                    if let error { Text(verbatim: error).foregroundStyle(.red).textSelection(.enabled) }
                }
            }.formStyle(.grouped).disabled(saving)
            Divider()
            HStack {
                if saving { ProgressView().controlSize(.small) }
                Spacer()
                Button("Cancel") { dismiss() }.keyboardShortcut(.cancelAction).disabled(saving)
                Button("Save") {
                    saving = true
                    Task {
                        do {
                            try await model.save(source: draft, replacing: original, torProxy: proxy)
                            dismiss()
                        } catch { self.error = error.localizedDescription; saving = false }
                    }
                }.keyboardShortcut(.defaultAction).disabled(saving)
            }.padding(16)
        }.frame(width: 620, height: 740).interactiveDismissDisabled(saving)
            .onAppear {
                do { catalog = try SourceCatalog.load() }
                catch { catalogError = error.localizedDescription }
            }
    }
}
