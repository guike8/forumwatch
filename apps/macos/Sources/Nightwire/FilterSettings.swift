import SwiftUI

struct AlertFilterControls: View {
    @Binding var filter: AlertFilter
    @State private var advanced = false
    var body: some View {
        Toggle(isOn: $filter.quietSales) {
            VStack(alignment: .leading, spacing: 4) {
                Text("Quiet account/combo sales")
                Text("Hide titles advertising combo lists or Gmail, Outlook, Hotmail, Yahoo, Netflix, and Spotify accounts. Does not scan post bodies or replace custom rules.")
                    .font(.caption).foregroundStyle(.secondary)
            }
        }
        .accessibilityIdentifier("filter.quietSales")
        Toggle(isOn: $filter.readableOnly) {
            VStack(alignment: .leading, spacing: 4) {
                Text("Readable posts only")
                Text("Hide restricted entries and posts with no readable text. Partial posts may still have hidden links.")
                    .font(.caption).foregroundStyle(.secondary)
            }
        }
        .accessibilityIdentifier("filter.readableOnly")
        DisclosureGroup("Advanced regular expressions", isExpanded: $advanced) {
            VStack(alignment: .leading, spacing: 12) {
                TextField("Include title/body (Go regex)", text: $filter.include)
                    .accessibilityIdentifier("filter.include")
                TextField("Exclude title/body (Go regex)", text: $filter.exclude)
                    .accessibilityIdentifier("filter.exclude")
                TextField("Exclude title (Go regex)", text: $filter.excludeTitle)
                    .accessibilityIdentifier("filter.excludeTitle")
                Text("Empty means no constraint. Go/RE2 syntax; use (?i) for case-insensitive matching. Exclusions win over inclusion. Rules are checked by the bundled backend before saving.")
                    .font(.caption).foregroundStyle(.secondary)
            }
            .textFieldStyle(.roundedBorder)
            .padding(.vertical, 8)
        }
        .accessibilityIdentifier("filter.advanced")
    }
}

struct FilterSettings: View {
    @ObservedObject var model: AppModel
    @Environment(\.dismiss) private var dismiss
    @State private var sourceID: String
    @State private var drafts: [String: AlertFilter]
    @State private var saving = false
    @State private var error: String?
    private let original: [String: AlertFilter]

    init(model: AppModel, sourceID: String?) {
        self.model = model
        let filters = Dictionary(uniqueKeysWithValues: model.sources.map { ($0.id, $0.alertFilter) })
        original = filters
        _drafts = State(initialValue: filters)
        _sourceID = State(initialValue: sourceID.flatMap { filters[$0] == nil ? nil : $0 } ?? model.sources.first?.id ?? "")
    }
    private var selectedSource: Source? { model.sources.first { $0.id == sourceID } }
    private var changes: [String: AlertFilter] { drafts.filter { original[$0.key] != $0.value } }
    private var filter: Binding<AlertFilter> {
        Binding(get: { drafts[sourceID] ?? AlertFilter() }, set: { drafts[sourceID] = $0 })
    }
    private var busy: Bool { saving || model.phase == .starting || model.phase == .stopping }
    var body: some View {
        VStack(alignment: .leading, spacing: 0) {
            VStack(alignment: .leading, spacing: 8) {
                Text("Filters").font(.title2)
                Text("Choose what reaches your feed. Rules are per source, never global by default.")
                    .font(.callout).foregroundStyle(.secondary)
            }.padding(20)
            if model.sources.isEmpty {
                ContentUnavailableView("No sources configured", systemImage: "line.3.horizontal.decrease.circle",
                                       description: Text("Add a source before configuring filters."))
            } else {
                Form {
                    Section("Source") {
                        Picker("Apply rules to", selection: $sourceID) {
                            ForEach(model.sources) { source in
                                Text(verbatim: source.displayName).tag(source.id)
                            }
                        }.accessibilityIdentifier("filter.source")
                        if let selectedSource {
                            Text(verbatim: selectedSource.url)
                                .font(.caption).foregroundStyle(.secondary).lineLimit(2).textSelection(.enabled)
                        }
                    }
                    Section("Delivery rules") {
                        AlertFilterControls(filter: filter)
                    }
                    Section {
                        Text("The quiet-sales heuristic can hide relevant posts. Review results after applying. Filters affect the API, live feed, and local extracts—not crawling or saved raw posts.")
                            .font(.caption).foregroundStyle(.secondary)
                        if let status = model.statuses.first(where: { $0.id == sourceID }),
                           let matching = status.matchingItems, let filtered = status.filteredItems {
                            Text("Currently applied rules: \(matching) matching · \(filtered) filtered saved revisions. Not a preview of unsaved changes.")
                                .font(.caption).foregroundStyle(.secondary)
                        }
                        Button("Clear filters for this source") { drafts[sourceID] = AlertFilter() }
                            .disabled(!(drafts[sourceID]?.isActive ?? false))
                    }
                    if let error {
                        Section("Could not apply filters") {
                            Text(verbatim: error).foregroundStyle(.red).textSelection(.enabled)
                        }
                    }
                }
                .formStyle(.grouped)
                .disabled(busy)
            }
            Divider()
            VStack(alignment: .leading, spacing: 12) {
                Text("Applying briefly restarts running monitoring. Stopped monitoring stays stopped. Source IDs, crawl progress, and saved posts are retained.")
                    .font(.caption).foregroundStyle(.secondary)
                HStack {
                    if saving { ProgressView().controlSize(.small) }
                    Spacer()
                    Button("Cancel") { dismiss() }.keyboardShortcut(.cancelAction).disabled(saving)
                    Button("Apply") {
                        saving = true
                        error = nil
                        Task {
                            do {
                                try await model.saveFilters(changes)
                                dismiss()
                            } catch {
                                self.error = error.localizedDescription
                                saving = false
                            }
                        }
                    }
                    .keyboardShortcut(.defaultAction)
                    .accessibilityIdentifier("filter.apply")
                    .disabled(busy || changes.isEmpty)
                }
            }.padding(16)
        }
        .frame(width: 560, height: 640)
        .interactiveDismissDisabled(saving)
    }
}
