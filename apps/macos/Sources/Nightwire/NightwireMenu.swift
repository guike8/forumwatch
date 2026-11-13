import SwiftUI
import AppKit

struct NightwireMenu: View {
    @ObservedObject var model: AppModel
    @Environment(\.openWindow) private var openWindow
    private var active: Bool { model.phase == .running || model.phase == .starting }

    var body: some View {
        Text("Nightwire · \(model.phase.rawValue)")
        if model.sources.isEmpty {
            Text("No sources configured")
        } else if model.phase == .running {
            Text("Source coverage: check source status in the window")
        }
        Divider()
        Button("Show Nightwire") {
            openWindow(id: "main")
            NSApplication.shared.activate(ignoringOtherApps: true)
        }
        Button(active ? "Stop monitoring" : "Start monitoring") {
            Task {
                if active { await model.stop() }
                else { await model.start() }
            }
        }
        .disabled(model.phase == .stopping || model.configuration == nil || (!active && model.sources.isEmpty))
        Divider()
        Button("Quit Nightwire") { NSApplication.shared.terminate(nil) }
            .keyboardShortcut("q", modifiers: .command)
    }
}
