import Foundation
import CryptoKit

// Bonjour is only an address hint. The scanned wing ID and key remain mandatory.
@MainActor final class PhoneBonjourLookup: NSObject, @preconcurrency NetServiceBrowserDelegate, @preconcurrency NetServiceDelegate {
    private let wingID: String, key: String
    private var continuation: CheckedContinuation<URL?, Never>?
    private var browser: NetServiceBrowser?
    private var service: NetService?
    private var timeout: Task<Void, Never>?
    init(wingID: String, key: String) { self.wingID = wingID; self.key = key }
    func resolve() async -> URL? {
        await withCheckedContinuation { continuation in
            self.continuation = continuation
            let browser = NetServiceBrowser(); self.browser = browser; browser.delegate = self
            browser.searchForServices(ofType: "_wingthing-phone._tcp.", inDomain: "local.")
            timeout = Task { try? await Task.sleep(for: .seconds(2)); self.finish(nil) }
        }
    }
    func netServiceBrowser(_ browser: NetServiceBrowser, didFind service: NetService, moreComing: Bool) {
        do {
            let hash = SHA256.hash(data: Data(self.wingID.utf8)).prefix(8).map { String(format: "%02x", $0) }.joined()
            guard self.continuation != nil, service.name == "wt-" + hash else { return }
            self.service = service; service.delegate = self; service.resolve(withTimeout: 2)
        }
    }
    func netServiceDidResolveAddress(_ sender: NetService) {
        do {
            guard let txt = sender.txtRecordData(), let host = sender.hostName, sender.port > 0 else { return }
            let fields = NetService.dictionary(fromTXTRecord: txt)
            guard fields["wing_id"] == Data(self.wingID.utf8), fields["key"] == Data(self.key.utf8),
                  let url = URL(string: "http://\(host.trimmingCharacters(in: CharacterSet(charactersIn: "."))):\(sender.port)"), PairingInvitation.localAllowed(url) else { return }
            self.finish(url)
        }
    }
    private func finish(_ url: URL?) {
        guard let continuation else { return }; self.continuation = nil
        timeout?.cancel(); timeout = nil; browser?.stop(); browser?.delegate = nil; browser = nil
        service?.stop(); service?.delegate = nil; service = nil
        continuation.resume(returning: url)
    }
}
