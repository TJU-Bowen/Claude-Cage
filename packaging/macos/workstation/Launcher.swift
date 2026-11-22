import AppKit
import Network

@main
final class WorkstationLauncher: NSObject, NSApplicationDelegate {
    private var browser: NWBrowser?

    static func main() {
        let app = NSApplication.shared
        let launcher = WorkstationLauncher()
        app.delegate = launcher
        app.setActivationPolicy(.accessory)
        withExtendedLifetime(launcher) { app.run() }
    }

    func applicationDidFinishLaunching(_ notification: Notification) {
        // Let macOS attach the local network permission to the Finder application.
        // The LAN helper still owns discovery, peer validation and pairing.
        let browser = NWBrowser(for: .bonjour(type: "_claude-cage._tcp", domain: "local."), using: .tcp)
        browser.stateUpdateHandler = { [weak self] state in
            switch state {
            case .ready, .failed:
                Task { @MainActor in
                    self?.browser?.cancel()
                    self?.browser = nil
                }
            default:
                break
            }
        }
        self.browser = browser
        browser.start(queue: .main)
        launchWorkbench()
    }

    func applicationShouldHandleReopen(_ sender: NSApplication, hasVisibleWindows flag: Bool) -> Bool {
        launchWorkbench()
        return false
    }

    private func launchWorkbench() {
        do {
            let helper = Bundle.main.bundleURL.appendingPathComponent("Contents/MacOS/CageWorkstationLAN")
            let process = Process()
            process.executableURL = helper
            process.arguments = ["panel"]
            try process.run()
        } catch {
            let alert = NSAlert(error: error)
            alert.messageText = "无法打开 Cage 工作台"
            alert.runModal()
        }
    }
}
