import Foundation
import Testing
@testable import WingthingCore

private let lanRoute = URL(string: "http://192.168.1.40:6078")!
private let otherLANRoute = URL(string: "http://192.168.1.41:6078")!
private let tailnetRoute = URL(string: "http://100.91.2.3:6078")!
private let magicDNSRoute = URL(string: "http://ehrlichmac.fixture.TS.NET:6078")!
private let discoveredRoute = URL(string: "http://ehrlichmac.local:6078")!
private let relayRoute = URL(string: "https://relay.fixture.invalid")!

private actor EndpointFixture {
    private let identity = TunnelIdentity()
    private var reachable: URL?
    private let discovered: URL?
    private let corrupt: Bool
    private let statusCode: Int
    private(set) var attempts: [String] = []
    var key: String { identity.publicKey }

    init(reachable: URL? = nil, discovered: URL? = nil, corrupt: Bool = false, statusCode: Int = 200) {
        self.reachable = reachable; self.discovered = discovered; self.corrupt = corrupt; self.statusCode = statusCode
    }
    func setReachable(_ route: URL?) { reachable = route }
    func clearAttempts() { attempts = [] }
    func discovery(wingID: String, key: String) -> URL? {
        #expect(wingID == "fixture-wing"); #expect(key == identity.publicKey)
        attempts.append("bonjour"); return discovered
    }
    private func reply(_ box: JSONValue) throws -> JSONValue {
        let cipher = try identity.cipher(peerPublicKey: box["sender_pub"]!.string!, domain: "wt-phone-carrier-v1")
        let value = try JSONDecoder().decode(JSONValue.self, from: cipher.open(box["payload"]!.string!))
        #expect(value["op"]?.string != nil)
        return .object(["payload": .string(try cipher.seal(JSONEncoder().encode(JSONValue.object(["ok": .bool(true)]))))])
    }
    func direct(_ request: URLRequest) throws -> (Data, URLResponse) {
        let origin = URL(string: "\(request.url!.scheme!)://\(request.url!.host!):\(request.url!.port!)")!
        attempts.append(origin.absoluteString)
        #expect(request.httpMethod == "POST"); #expect(request.url!.path == "/phone")
        #expect(request.value(forHTTPHeaderField: "Authorization") == nil)
        #expect(request.value(forHTTPHeaderField: "Cookie") == nil)
        guard origin == reachable else { throw URLError(.cannotConnectToHost) }
        if statusCode != 200 { return (Data(), HTTPURLResponse(url: request.url!, statusCode: statusCode, httpVersion: "HTTP/1.1", headerFields: nil)!) }
        let box = try JSONDecoder().decode(JSONValue.self, from: request.httpBody!)
        #expect(box["op"]?.string == "phone.box")
        let response = corrupt ? JSONValue.object(["payload": .string("invalid-ciphertext")]) : try reply(box)
        return (try JSONEncoder().encode(response), HTTPURLResponse(url: request.url!, statusCode: 200, httpVersion: "HTTP/1.1", headerFields: nil)!)
    }
    func relay(_ box: JSONValue, origin: URL, wingID: String) throws -> JSONValue {
        attempts.append("relay")
        #expect(origin == relayRoute); #expect(wingID == "fixture-wing")
        return try reply(box)
    }
    func wire() -> URLSessionPhoneWire {
        URLSessionPhoneWire(directExchange: { try await self.direct($0) },
                            discover: { await self.discovery(wingID: $0, key: $1) },
                            relayExchange: { try await self.relay($0, origin: $1, wingID: $2) })
    }
}

@Suite struct PhoneEndpointTests {
    private func request(_ fixture: EndpointFixture, wire: URLSessionPhoneWire, relay: URL? = relayRoute, op: String = "device.hello") async throws -> JSONValue {
        try await wire.request(.object(["op": .string(op)]), local: lanRoute, relay: relay, wingID: "fixture-wing", wingKey: fixture.key)
    }
    private func configured(_ fixture: EndpointFixture) async -> URLSessionPhoneWire {
        let wire = await fixture.wire()
        // Deliberately shuffled and duplicated: the client owns preference order.
        await wire.configure(endpoints: [magicDNSRoute, tailnetRoute, otherLANRoute, lanRoute, tailnetRoute])
        return wire
    }
    @Test func lanSucceedsBeforeDiscoveryTailnetAndRegisteredRelay() async throws {
        let fixture = EndpointFixture(reachable: lanRoute), wire = await configured(fixture)
        #expect(try await request(fixture, wire: wire)["ok"] == .bool(true))
        #expect(await fixture.attempts == [lanRoute.absoluteString])
    }
    @Test func bonjourRecoversChangedLANBeforeTailnet() async throws {
        let fixture = EndpointFixture(reachable: discoveredRoute, discovered: discoveredRoute), wire = await configured(fixture)
        #expect(try await request(fixture, wire: wire, relay: nil)["ok"] == .bool(true))
        #expect(await fixture.attempts == [lanRoute.absoluteString, otherLANRoute.absoluteString, "bonjour", discoveredRoute.absoluteString])
    }
    @Test func tailnetIPNeedsNoAccountOrRelay() async throws {
        let fixture = EndpointFixture(reachable: tailnetRoute), wire = await fixture.wire()
        await wire.configure(endpoints: [tailnetRoute, magicDNSRoute])
        #expect(try await request(fixture, wire: wire, relay: nil)["ok"] == .bool(true))
        #expect(await fixture.attempts == [lanRoute.absoluteString, "bonjour", tailnetRoute.absoluteString])
    }
    @Test func magicDNSNeedsNoAccountAndIsClassifiedWithoutCaseSensitivity() async throws {
        let fixture = EndpointFixture(reachable: magicDNSRoute), wire = await fixture.wire()
        await wire.configure(endpoints: [tailnetRoute, magicDNSRoute])
        #expect(try await request(fixture, wire: wire, relay: nil)["ok"] == .bool(true))
        #expect(await fixture.attempts == [lanRoute.absoluteString, "bonjour", tailnetRoute.absoluteString, magicDNSRoute.absoluteString])
    }
    @Test func registeredRelayRunsOnlyAfterEveryDirectRouteFails() async throws {
        let fixture = EndpointFixture(), wire = await fixture.wire()
        await wire.configure(endpoints: [tailnetRoute, magicDNSRoute])
        #expect(try await request(fixture, wire: wire)["ok"] == .bool(true))
        #expect(await fixture.attempts == [lanRoute.absoluteString, "bonjour", tailnetRoute.absoluteString, magicDNSRoute.absoluteString, "relay"])
    }
    @Test func localOnlyLANNeverContactsAccountDiscoveryOrRelay() async throws {
        let fixture = EndpointFixture(reachable: lanRoute), wire = await configured(fixture)
        #expect(try await request(fixture, wire: wire, relay: nil, op: "pair.start")["ok"] == .bool(true))
        #expect(await fixture.attempts == [lanRoute.absoluteString])
    }
    @Test func unregisteredUnreachableWingNeverFallsBackToRelay() async throws {
        let fixture = EndpointFixture(), wire = await fixture.wire()
        await wire.configure(endpoints: [tailnetRoute, magicDNSRoute])
        do { _ = try await request(fixture, wire: wire, relay: nil); Issue.record("Unreachable direct wing accepted") }
        catch { #expect((error as? URLError)?.code == .cannotConnectToHost) }
        #expect(await fixture.attempts == [lanRoute.absoluteString, "bonjour", tailnetRoute.absoluteString, magicDNSRoute.absoluteString])
    }
    @Test func authenticationFailureNeverDowngradesToAnotherRoute() async throws {
        let fixture = EndpointFixture(reachable: lanRoute, corrupt: true), wire = await configured(fixture)
        do { _ = try await request(fixture, wire: wire); Issue.record("Unauthenticated carrier accepted") }
        catch { #expect(error as? PairingError == .badProof) }
        #expect(await fixture.attempts == [lanRoute.absoluteString])
    }
    @Test func unavailableDirectHandshakeCanTryRegisteredRelay() async throws {
        let fixture = EndpointFixture(reachable: lanRoute, statusCode: 503), wire = await fixture.wire()
        #expect(try await request(fixture, wire: wire)["ok"] == .bool(true))
        #expect(await fixture.attempts == [lanRoute.absoluteString, "bonjour", "relay"])
    }
    @Test func forbiddenDirectHandshakeNeverDowngrades() async throws {
        let fixture = EndpointFixture(reachable: lanRoute, statusCode: 403), wire = await fixture.wire()
        do { _ = try await request(fixture, wire: wire); Issue.record("Forbidden carrier accepted") }
        catch { #expect(error as? PairingError == .protocolFailure) }
        #expect(await fixture.attempts == [lanRoute.absoluteString])
    }
    @Test func invalidCurvePinHardStopsBeforeAnyCarrierRequest() async throws {
        let fixture = EndpointFixture(reachable: lanRoute), wire = await fixture.wire()
        do {
            _ = try await wire.request(.object(["op": .string("pair.start")]), local: lanRoute, relay: relayRoute, wingID: "fixture-wing", wingKey: Data(repeating: 0, count: 32).base64EncodedString())
            Issue.record("Low-order curve pin accepted")
        } catch { #expect(error as? PairingError == .badProof) }
        #expect(await fixture.attempts.isEmpty)
    }
    @Test func newHandshakeRetriesLANBeforeCachedTailnet() async throws {
        let fixture = EndpointFixture(reachable: tailnetRoute), wire = await configured(fixture)
        _ = try await request(fixture, wire: wire)
        await fixture.clearAttempts(); await fixture.setReachable(lanRoute)
        #expect(try await request(fixture, wire: wire)["ok"] == .bool(true))
        #expect(await fixture.attempts == [lanRoute.absoluteString])
    }
    @Test func failedSessionCommandStaysOnItsSelectedCarrier() async throws {
        let fixture = EndpointFixture(reachable: tailnetRoute), wire = await configured(fixture)
        _ = try await request(fixture, wire: wire)
        await fixture.clearAttempts(); await fixture.setReachable(lanRoute)
        do { _ = try await request(fixture, wire: wire, op: "device.command"); Issue.record("Uncertain session command replayed") }
        catch { #expect((error as? URLError)?.code == .cannotConnectToHost) }
        #expect(await fixture.attempts == [tailnetRoute.absoluteString])
    }
}
