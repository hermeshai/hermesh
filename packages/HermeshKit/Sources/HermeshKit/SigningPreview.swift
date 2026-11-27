import CryptoKit
import Foundation
import HermeshProto
import SwiftProtobuf

public typealias SigningRequest = Hermesh_Local_V1_SigningRequest
public typealias PairingKind = Hermesh_Identity_V1_PairingKind

/// Who started a signing request: the local process hermeshd saw make the
/// call. A hint for the person, not proof.
public struct CallerInfo: Equatable {
    /// The program's file name; nil for the Hermesh app itself, "" when
    /// unknown.
    public let program: String?
    /// Its executable path, shown with the warning ("" when unknown).
    public var path = ""

    public init(program: String?, path: String = "") {
        self.program = program
        self.path = path
    }

    public var isHermesh: Bool { program == nil }

    public var label: String {
        switch program {
        case nil: return String(localized: "Requested by the Hermesh app", bundle: .hermeshKit)
        case "": return String(localized: "Requested by an unknown program", bundle: .hermeshKit)
        case let p?: return String(localized: "Requested by \(p)", bundle: .hermeshKit, comment: "%@ is a program's file name")
        }
    }
}

/// One request as the confirmation panel shows it.
public struct Card: Codable, Equatable {
    public var type: String
    public var title: String // who wants what
    public var who: String // who is asking / who it is about ("": not shown)
    /// A verification code the person compares with what the other side
    /// shows (pairing approvals).
    public var code: String?
    public var lines: [String]
    /// What stays on the card when there are rows: the scope as label and value (and any limit beyond
    /// the defaults), in place of lines, which then go behind ⓘ with the ids.
    public var rows: [CardRow] = []
    public var why: String // what approving does
    public var warning: String?
    /// Identifiers (ids, fingerprints) for whoever wants them, behind ⓘ.
    public var ids: [String] = []
}

public struct CardRow: Codable, Equatable {
    /// What the row is about: gives it its label (and, in the panel, its icon).
    public enum Kind: String, Codable, CaseIterable {
        case project, machine, codingAgent, expires, device, trustedDevices, extra

        public var label: String {
            switch self {
            case .project: return String(localized: "Projects", bundle: .hermeshKit)
            case .machine: return String(localized: "Machines", bundle: .hermeshKit)
            case .codingAgent: return String(localized: "Coding agents", bundle: .hermeshKit)
            case .expires: return String(localized: "Expires", bundle: .hermeshKit)
            case .device: return String(localized: "Device", bundle: .hermeshKit)
            case .trustedDevices: return String(localized: "Trusted devices", bundle: .hermeshKit)
            case .extra: return String(localized: "Also", bundle: .hermeshKit, comment: "Label of a card row with a limit beyond the defaults")
            }
        }
    }

    public var kind: Kind
    public var value: String
    public var label: String { kind.label }

    public init(kind: Kind, value: String) {
        self.kind = kind
        self.value = value
    }
}

/// Why a request cannot be shown in full, so the signer refuses it.
public enum Refusal: Error, Equatable {
    case messageMismatch(String)
    case unknownType(String)
    case unparseable(String)
    case unknownFields(String)
    case unsupported(String)

    public var text: String {
        switch self {
        case .messageMismatch(let t): return String(localized: "What would be signed doesn’t match what would be shown (\(t)).", bundle: .hermeshKit, comment: "%@ is a request type")
        case .unknownType(let t): return String(localized: "Hermesh doesn’t recognize this kind of request (\(t)).", bundle: .hermeshKit)
        case .unparseable(let t): return String(localized: "The request can’t be parsed (\(t)).", bundle: .hermeshKit)
        case .unknownFields(let t): return String(localized: "The request contains something Hermesh doesn’t recognize (\(t)). Hermesh may need an update.", bundle: .hermeshKit)
        case .unsupported(let t): return String(localized: "Hermesh can’t show this request in full (\(t)).", bundle: .hermeshKit)
        }
    }
}

/// Renders what the owner is about to sign, parsed by the signer itself
/// from the exact bytes that will be signed (never from text hermeshd
/// supplies). A request it cannot show in full is refused.
/// The device a preview is shown on, for the words that name it.
public enum DeviceKind: Sendable {
    case mac, iPhone
}

public enum SigningPreview {
    /// Where this preview runs; the iPhone app sets .iPhone at launch.
    nonisolated(unsafe) public static var device: DeviceKind = .mac

    /// The Mac's words or the iPhone's.
    private static func on(_ mac: @autoclosure () -> String, _ iPhone: @autoclosure () -> String) -> String {
        device == .mac ? mac() : iPhone()
    }

    public static let ownerDeviceCert = "hermesh.identity.v1.OwnerDeviceCert"
    public static let rootCert = "hermesh.identity.v1.RootCert"
    public static let pairingRequest = "hermesh.identity.v1.PairingRequest"
    public static let machineCert = "hermesh.identity.v1.MachineCert"
    public static let grant = "hermesh.identity.v1.Grant"
    public static let adminCommand = "hermesh.identity.v1.AdminCommand"
    public static let eraseOrder = "hermesh.identity.v1.EraseOrder"

    public static func signingInput(type: String, body: Data) -> Data {
        var d = Data("hermesh-sig-v1\0".utf8)
        d.append(Data(type.utf8))
        d.append(0)
        d.append(body)
        return d
    }

    /// Display hints from hermeshd's lists, for names and "before" values.
    /// Only the ids come from the signed bytes.
    public struct Hints {
        public var machineNames: [String: String] = [:]
        public var clients: [String: Hermesh_Local_V1_Client] = [:] // by grant id
        /// Trusted devices (key id → name); nil when they could not be listed.
        public var devices: [String: String]?

        public init(machineNames: [String: String] = [:], clients: [String: Hermesh_Local_V1_Client] = [:], devices: [String: String]? = nil) {
            self.machineNames = machineNames
            self.clients = clients
            self.devices = devices
        }
    }

    /// The cards of a batch, in order. Throws the refusals when any request
    /// cannot be shown in full: the whole batch is then refused.
    public static func cards(_ reqs: [SigningRequest], _ hints: Hints = .init()) throws -> [Card] {
        let reissue = isReissue(reqs, hints)
        var cards: [Card] = []
        var refusals: [Refusal] = []
        for r in reqs {
            do {
                cards.append(try card(r, hints, reissue: reissue))
            } catch let e as Refusal {
                refusals.append(e)
            } catch {
                refusals.append(.unsupported(r.type))
            }
        }
        if !refusals.isEmpty { throw RefusalList(refusals: refusals) }
        return cards
    }

    public struct RefusalList: Error, Equatable { public let refusals: [Refusal] }

    /// A batch that removes a trusted device re-issues, unchanged, what that
    /// device had signed. Told from the signed root cert, not from hermeshd.
    public static func isReissue(_ reqs: [SigningRequest], _ hints: Hints) -> Bool {
        guard reqs.count > 1, let devices = hints.devices,
              let r = reqs.first(where: { $0.type == rootCert }),
              let c = try? strict(r, Hermesh_Identity_V1_RootCert.self), c.version > 1 else { return false }
        let keys = Set(c.rootKeys.map { keyID($0.publicKey) })
        return devices.keys.contains { !keys.contains($0) }
    }

    /// The panel's one line for the first setup.
    public static var firstSetupSummary: String {
        on(String(localized: "Creates your owner key in this Mac’s Secure Enclave. Without it, no one can use the services on your computers, and only you can approve, with Touch ID.", bundle: .hermeshKit),
           String(localized: "Creates your owner key in this iPhone’s Secure Enclave. Without it, no one can use the services on your computers, and only you can approve, with Face ID.", bundle: .hermeshKit))
    }

    /// The owner's first setup, told from the signed bytes: the first root
    /// cert with its one key, this Mac's device cert, and (if allowed) this
    /// Mac's own machine cert, all for that owner. It grants nobody else
    /// anything, so the panel can say it in one line.
    public static func isFirstSetup(_ reqs: [SigningRequest]) -> Bool {
        let roots = reqs.filter { $0.type == rootCert }
        guard roots.count == 1, let c = try? strict(roots[0], Hermesh_Identity_V1_RootCert.self), c.version == 1, c.rootKeys.count == 1 else { return false }
        let key = keyID(c.rootKeys[0].publicKey)
        return reqs.allSatisfy { r in
            switch r.type {
            case rootCert: return true
            case ownerDeviceCert: return (try? strict(r, Hermesh_Identity_V1_OwnerDeviceCert.self))?.ownerID == c.ownerID
            case machineCert:
                guard let m = try? strict(r, Hermesh_Identity_V1_MachineCert.self) else { return false }
                return m.ownerID == c.ownerID && m.deviceKeyID == key
            default: return false
            }
        }
    }

    /// Parses body as M, dropping fields this signer does not know; what
    /// was parsed must then re-serialize to exactly the bytes to be signed,
    /// so a body with an unknown field (or any encoding hermeshd would not
    /// produce) is refused. hermeshd marshals deterministically, a map's
    /// entries sorted by key (a connector's environment), as this does.
    public static func strict<M: SwiftProtobuf.Message>(_ r: SigningRequest, _: M.Type) throws -> M {
        var options = BinaryDecodingOptions()
        options.discardUnknownFields = true
        let m: M
        do {
            m = try M(serializedBytes: r.body, options: options)
        } catch {
            throw Refusal.unparseable(r.type)
        }
        var encoding = BinaryEncodingOptions()
        encoding.useDeterministicOrdering = true
        guard let again: Data = try? m.serializedBytes(options: encoding), again == r.body else { throw Refusal.unknownFields(r.type) }
        return m
    }

    public static func card(_ r: SigningRequest, _ hints: Hints = .init(), reissue: Bool = false) throws -> Card {
        guard r.message == signingInput(type: r.type, body: r.body) else { throw Refusal.messageMismatch(r.type) }
        switch r.type {
        case ownerDeviceCert:
            let m = try strict(r, Hermesh_Identity_V1_OwnerDeviceCert.self)
            return Card(type: r.type, title: on(String(localized: "Issue this Mac a certificate for end-to-end connections", bundle: .hermeshKit),
                           String(localized: "Issue this iPhone a certificate for end-to-end connections", bundle: .hermeshKit)), who: "", lines: [],
                        why: on(String(localized: "Once approved, Hermesh on this Mac uses it for end-to-end encrypted connections to your machines, to view and manage your tasks and machines.", bundle: .hermeshKit),
                           String(localized: "Once approved, Hermesh on this iPhone uses it for end-to-end encrypted connections to your machines, to view and manage your tasks and machines.", bundle: .hermeshKit)),
                        ids: [String(localized: "Device name: \(m.label)", bundle: .hermeshKit), String(localized: "Fingerprint: \(fingerprint(m.devicePublicKey))", bundle: .hermeshKit)])
        case rootCert:
            var c = describeRoot(try strict(r, Hermesh_Identity_V1_RootCert.self), hints)
            // A device found by the code it shows: that code binds its key.
            if r.confirmed { c.code = nil }
            return c
        case pairingRequest:
            let p = try strict(r, Hermesh_Identity_V1_PairingRequest.self)
            guard p.kind == .device else { throw Refusal.unsupported(r.type) }
            return Card(type: r.type, title: on(String(localized: "Add this Mac to your Hermesh", bundle: .hermeshKit),
                           String(localized: "Add this iPhone to your Hermesh", bundle: .hermeshKit)), who: "",
                        code: verificationCode(.device, p.subjectPublicKey, p.ownerID), lines: [],
                        why: on(String(localized: "This sends a join request to your existing devices. Check the same code on one of them and approve, and this Mac becomes a trusted device.", bundle: .hermeshKit),
                           String(localized: "This sends a join request to your existing devices. Check the same code on one of them and approve, and this iPhone becomes a trusted device.", bundle: .hermeshKit)),
                        ids: [String(localized: "Device name: \(p.label)", bundle: .hermeshKit), String(localized: "Owner identity ID: \(p.ownerID)", bundle: .hermeshKit)])
        case machineCert:
            var c = describeMachine(try strict(r, Hermesh_Identity_V1_MachineCert.self), hints, reissue: reissue)
            // Found by the code its terminal shows: that code binds its key,
            // so there is nothing to compare.
            if r.confirmed { c.code = nil }
            return c
        case grant:
            let g = try strict(r, Hermesh_Identity_V1_Grant.self)
            if case .UNRECOGNIZED = g.taskVisibility { throw Refusal.unsupported(r.type) }
            if case .UNRECOGNIZED = g.maxRuntimeMode { throw Refusal.unsupported(r.type) }
            var c = describeGrant(g, hints, reissue: reissue)
            // The person already said, signed in to their account, that this
            // agent is theirs: its side shows no code to compare.
            if r.confirmed { c.code = nil }
            return c
        case adminCommand:
            return try describeAdmin(try strict(r, Hermesh_Identity_V1_AdminCommand.self), hints)
        case eraseOrder:
            let e = try strict(r, Hermesh_Identity_V1_EraseOrder.self)
            return Card(type: r.type, title: String(localized: "Erase everything in your Hermesh", bundle: .hermeshKit), who: "", lines: [],
                        why: on(String(localized: "Once approved, Hermesh on all your trusted devices and machines, this Mac included, erases its keys, tasks and settings, and your agents are disconnected. This can’t be undone.", bundle: .hermeshKit),
                           String(localized: "Once approved, Hermesh on all your trusted devices and machines, this iPhone included, erases its keys, tasks and settings, and your agents are disconnected. This can’t be undone.", bundle: .hermeshKit)),
                        warning: String(localized: "Approve only if you started erasing everything yourself.", bundle: .hermeshKit),
                        ids: [String(localized: "Owner identity ID: \(e.ownerID)", bundle: .hermeshKit), String(localized: "Erase ID: \(e.eraseID)", bundle: .hermeshKit)])
        default:
            throw Refusal.unknownType(r.type)
        }
    }

    /// The owner's trusted devices change: compared with the devices
    /// hermeshd lists, by the key ids computed from the signed bytes.
    private static func describeRoot(_ c: Hermesh_Identity_V1_RootCert, _ hints: Hints) -> Card {
        let type = rootCert
        let keys = c.rootKeys.map { (id: keyID($0.publicKey), key: $0) }
        let names = keys.map { $0.key.label.isEmpty ? String(localized: "Unnamed device", bundle: .hermeshKit) : $0.key.label }
        if c.version == 1 {
            return Card(type: type, title: String(localized: "Create your owner identity", bundle: .hermeshKit), who: "", lines: [],
                        why: on(String(localized: "Once approved, this Mac becomes your first trusted device; any device that joins later must be approved by a trusted device.", bundle: .hermeshKit),
                           String(localized: "Once approved, this iPhone becomes your first trusted device; any device that joins later must be approved by a trusted device.", bundle: .hermeshKit)),
                        ids: [String(localized: "Owner identity ID: \(c.ownerID)", bundle: .hermeshKit), String(localized: "Device name: \(names.first ?? "")", bundle: .hermeshKit)])
        }
        let lines = [String(localized: "Your trusted devices after this: \(list(names))", bundle: .hermeshKit), String(localized: "Version \(Int(c.version))", bundle: .hermeshKit)]
        guard let devices = hints.devices else {
            return Card(type: type, title: String(localized: "Update your list of trusted devices", bundle: .hermeshKit), who: String(localized: "Your trusted devices", bundle: .hermeshKit), lines: lines,
                        why: String(localized: "Once approved, the list above becomes your trusted devices.", bundle: .hermeshKit))
        }
        let added = keys.filter { devices[$0.id] == nil }
        let removed = devices.filter { d in !keys.contains { $0.id == d.key } }.sorted { $0.key < $1.key }
        if let a = added.first {
            return Card(type: type, title: String(localized: "Add “\(a.key.label)” as a trusted device", bundle: .hermeshKit), who: "",
                        code: verificationCode(.device, a.key.publicKey, c.ownerID), lines: lines,
                        rows: [CardRow(kind: .device, value: a.key.label),
                               CardRow(kind: .trustedDevices, value: String(localized: "\(keys.count) in all once it joins", bundle: .hermeshKit, comment: "The number of trusted devices"))],
                        why: on(String(localized: "Uses your owner key to make it a trusted device too, able to approve agents, machines and new devices just like this Mac.", bundle: .hermeshKit),
                           String(localized: "Uses your owner key to make it a trusted device too, able to approve agents, machines and new devices just like this iPhone.", bundle: .hermeshKit)),
                        warning: String(localized: "Approve only if that device is in your hands.", bundle: .hermeshKit))
        }
        if let r = removed.first {
            return Card(type: type, title: String(localized: "Remove trusted device “\(r.value)”", bundle: .hermeshKit), who: String(localized: "Device “\(r.value)”", bundle: .hermeshKit), lines: lines,
                        why: on(String(localized: "Once approved, it can no longer approve anything, and its own machine is removed with it; this Mac reissues the machine and personal agent credentials it signed.", bundle: .hermeshKit),
                           String(localized: "Once approved, it can no longer approve anything, and its own machine is removed with it; this iPhone reissues the machine and personal agent credentials it signed.", bundle: .hermeshKit)))
        }
        return Card(type: type, title: String(localized: "Update your list of trusted devices", bundle: .hermeshKit), who: String(localized: "Your trusted devices", bundle: .hermeshKit), lines: lines,
                        why: String(localized: "Once approved, the list above becomes your trusted devices.", bundle: .hermeshKit))
    }

    /// A machine cert is the machine's key: its name is a setting, shown
    /// here from hermeshd's list when the machine is already known.
    private static func describeMachine(_ m: Hermesh_Identity_V1_MachineCert, _ hints: Hints, reissue: Bool) -> Card {
        let type = machineCert
        let id = String(localized: "Machine ID: \(m.machineID)", bundle: .hermeshKit)
        if let name = hints.machineNames[m.machineID] {
            return Card(type: type, title: String(localized: "Reissue the certificate of machine “\(name)”", bundle: .hermeshKit), who: String(localized: "Machine “\(name)” (already yours)", bundle: .hermeshKit),
                        lines: [id], why: reissue ? on(String(localized: "Once approved, this machine stays yours, unchanged, now signed by this Mac.", bundle: .hermeshKit),
                           String(localized: "Once approved, this machine stays yours, unchanged, now signed by this iPhone.", bundle: .hermeshKit))
                            : String(localized: "Once approved, this machine stays yours.", bundle: .hermeshKit))
        }
        if !m.deviceKeyID.isEmpty {
            return Card(type: type, title: String(localized: "Share the agents on this Mac", bundle: .hermeshKit), who: "", lines: [],
                        why: String(localized: "Once approved, trusted clients you grant access can use and control the agents on this Mac, over end-to-end encrypted connections. You can turn this off in Settings at any time.", bundle: .hermeshKit),
                        ids: [id, String(localized: "Fingerprint: \(fingerprint(m.machinePublicKey))", bundle: .hermeshKit)])
        }
        return Card(type: type, title: String(localized: "Approve a new machine", bundle: .hermeshKit), who: String(localized: "Machine ID \(String(m.machineID.prefix(12)))…", bundle: .hermeshKit),
                    code: verificationCode(.machine, m.machinePublicKey, m.ownerID),
                    lines: [id, String(localized: "Fingerprint: \(fingerprint(m.machinePublicKey))", bundle: .hermeshKit)],
                    why: String(localized: "Once approved, this machine becomes yours, and agents you grant access can start tasks and run commands on it. Approve only if you know this machine; you can remove it later from the machine list.", bundle: .hermeshKit),
                    warning: String(localized: "If the verification code doesn’t match, this isn’t the machine in front of you.", bundle: .hermeshKit))
    }

    private static func describeGrant(_ g: Hermesh_Identity_V1_Grant, _ hints: Hints, reissue: Bool) -> Card {
        let type = grant
        let name = g.clientLabel
        let expires = Date(timeIntervalSince1970: TimeInterval(g.expiresAt.seconds))
        let projects = scope(g.projectIds)
        let machines = machineScope(g.machineIds, hints)
        let agents = scope(g.agents.map(codingAgentTitle))
        let when = expires.formatted(date: .abbreviated, time: .shortened)
        let day = expires.formatted(date: .abbreviated, time: .omitted)
        let fullWarning = String(localized: "Full trust: on the chosen machines, this personal agent can do anything you can, including reading credentials and accessing the network.", bundle: .hermeshKit)
        let id = String(localized: "Fingerprint: \(fingerprint(g.clientPublicKey))", bundle: .hermeshKit)
        // Limits beyond the defaults are part of the scope too.
        var limits: [String] = []
        if g.taskVisibility == .all { limits.append(String(localized: "Can also see other personal agents’ tasks in these projects", bundle: .hermeshKit)) }
        if g.maxConcurrentSessions > 0 { limits.append(String(localized: "Tasks at once: up to \(Int(g.maxConcurrentSessions))", bundle: .hermeshKit)) }
        switch g.maxRuntimeMode {
        case .unspecified: break
        case .supervised: limits.append(String(localized: "Autonomy limit: \(String(localized: "Ask before each file change and command", bundle: .hermeshKit))", bundle: .hermeshKit))
        case .autoEdits: limits.append(String(localized: "Autonomy limit: \(String(localized: "Change files without asking; ask before other commands", bundle: .hermeshKit))", bundle: .hermeshKit))
        case .auto: limits.append(String(localized: "Autonomy limit: \(String(localized: "Change files and run routine checks without asking", bundle: .hermeshKit))", bundle: .hermeshKit))
        case .fullAccess: limits.append(String(localized: "Autonomy limit: \(String(localized: "Full access: ask before nothing", bundle: .hermeshKit))", bundle: .hermeshKit))
        case .UNRECOGNIZED: break
        }
        var rows = [CardRow(kind: .project, value: projects), CardRow(kind: .codingAgent, value: agents),
                    CardRow(kind: .expires, value: String(localized: "Until \(day)", bundle: .hermeshKit))]
        if !isAll(g.machineIds) { rows.insert(CardRow(kind: .machine, value: machines), at: 1) }
        rows += limits.map { CardRow(kind: .extra, value: $0) }
        // An MCP connection: a server holds this agent's key for a remote app.
        let custodian = URL(string: g.custodian)?.host ?? g.custodian
        var custody: [String] = []
        if !custodian.isEmpty {
            custody = [String(localized: "Connects through \(custodian), which holds its key", bundle: .hermeshKit)]
            rows.append(CardRow(kind: .extra, value: custody[0]))
        }
        // Her's own grant (docs/her-mesh.md §3): she becomes one of the owner's
        // agents, on the machine she runs on, whose hermeshd keeps her key. No
        // code to compare: the key came from that machine, end to end. What she
        // may use and until when are read from the signed bytes.
        if !g.herMachineID.isEmpty {
            let her = hints.machineNames[g.herMachineID] ?? String(g.herMachineID.prefix(12))
            let noExpiry = g.expiresAt.seconds == 253_402_300_799
            let until = noExpiry ? String(localized: "Doesn’t expire", bundle: .hermeshKit) : String(localized: "Until \(day)", bundle: .hermeshKit)
            rows[rows.firstIndex { $0.kind == .expires }!] = CardRow(kind: .expires, value: until)
            return Card(type: type, title: String(localized: "Let Her on \(her) use your computers", bundle: .hermeshKit), who: "",
                        lines: [String(localized: "Projects it can use: \(projects)", bundle: .hermeshKit), String(localized: "On these machines: \(machines)", bundle: .hermeshKit),
                                String(localized: "Coding agents it can run: \(agents)", bundle: .hermeshKit)] + limits
                            + [noExpiry ? until : String(localized: "Expires: \(when)", bundle: .hermeshKit), id],
                        rows: rows,
                        why: String(localized: "Her becomes one of your agents: she can start coding agents in your projects, read their tasks and answer them. It ends when you turn Her off there.", bundle: .hermeshKit),
                        warning: String(localized: "Full trust: on these machines, a coding agent she starts can do anything you can.", bundle: .hermeshKit))
        }
        if g.version <= 1 {
            return Card(type: type, title: String(localized: "Approve “\(name)” to connect to your computer", bundle: .hermeshKit), who: "",
                        code: verificationCode(.client, g.clientPublicKey, g.ownerID),
                        lines: [String(localized: "Personal agent “\(name)” (\(id))", bundle: .hermeshKit), String(localized: "Projects it can use: \(projects)", bundle: .hermeshKit),
                                String(localized: "On these machines: \(machines)", bundle: .hermeshKit), String(localized: "Coding agents it can run: \(agents)", bundle: .hermeshKit)]
                            + limits + custody + [String(localized: "Expires: \(when)", bundle: .hermeshKit), fullWarning],
                        rows: rows,
                        why: custodian.isEmpty
                            ? String(localized: "Uses your owner key to issue it an end-to-end connection grant, which you can revoke at any time.", bundle: .hermeshKit)
                            : String(localized: "Uses your owner key to issue it a connection grant, which you can revoke at any time. \(custodian) holds this agent’s key: it can act as the agent within this access, and sees the tasks it reaches.", bundle: .hermeshKit),
                        warning: String(localized: "Full trust: in the chosen projects, it can do anything you can.", bundle: .hermeshKit))
        }
        let cur = hints.clients[g.grantID]
        let was = cur.map { (projects: scope($0.spec.projectIds), machines: machineScope($0.spec.machineIds, hints), agents: scope($0.spec.agents.map(codingAgentTitle))) }
        if reissue, let was, was.projects == projects, was.machines == machines, was.agents == agents {
            return Card(type: type, title: String(localized: "Reissue the grant of “\(name)”", bundle: .hermeshKit), who: String(localized: "Personal agent “\(name)” (\(id))", bundle: .hermeshKit),
                        lines: [String(localized: "Same access: \(projects); \(agents); expires \(when)", bundle: .hermeshKit)] + limits + [String(localized: "Version \(Int(g.version))", bundle: .hermeshKit)],
                        why: on(String(localized: "Once approved, it keeps the same access, now signed by this Mac; the old version stops working.", bundle: .hermeshKit),
                           String(localized: "Once approved, it keeps the same access, now signed by this iPhone; the old version stops working.", bundle: .hermeshKit)))
        }
        var lines: [String] = []
        if let cur, let was {
            if was.projects != projects { lines.append(String(localized: "Projects: \(was.projects) → \(projects)", bundle: .hermeshKit)) }
            if was.machines != machines { lines.append(String(localized: "Machines: \(was.machines) → \(machines)", bundle: .hermeshKit)) }
            if was.agents != agents { lines.append(String(localized: "Coding agents: \(was.agents) → \(agents)", bundle: .hermeshKit)) }
            lines += limits
            let before = cur.expiresAt.date.formatted(date: .abbreviated, time: .shortened)
            lines.append(String(localized: "Expires: \(when) (before the change: \(before))", bundle: .hermeshKit))
            if lines.count == 1 { lines.insert(String(localized: "Projects, machines and coding agents stay the same; only the expiry changes.", bundle: .hermeshKit), at: 0) }
            lines.append(String(localized: "(What applies before the change comes from Hermesh’s current list; what applies after it comes from what will be signed.)", bundle: .hermeshKit))
        } else {
            lines = [String(localized: "New settings:", bundle: .hermeshKit), String(localized: "Projects: \(projects)", bundle: .hermeshKit),
                     String(localized: "Machines: \(machines)", bundle: .hermeshKit), String(localized: "Coding agents: \(agents)", bundle: .hermeshKit)]
                + limits + [String(localized: "Expires: \(when)", bundle: .hermeshKit),
                            String(localized: "(Hermesh has no record of its settings before the change, so they can’t be compared.)", bundle: .hermeshKit)]
        }
        return Card(type: type, title: String(localized: "Change the access of personal agent “\(name)”", bundle: .hermeshKit),
                    who: String(localized: "Personal agent “\(name)” (\(id))", bundle: .hermeshKit), lines: lines,
                    why: String(localized: "Once approved, the new settings take effect at once (version \(Int(g.version))) and the old ones stop working; from then on it can act only within the new settings.", bundle: .hermeshKit),
                    warning: fullWarning)
    }

    private static func describeAdmin(_ c: Hermesh_Identity_V1_AdminCommand, _ hints: Hints) throws -> Card {
        let type = adminCommand
        let target = machineLabel(c.targetMachineID, hints)
        let who = String(localized: "Your owner key, instructing \(target)", bundle: .hermeshKit)
        switch c.command {
        case .addProject(let p):
            let mode: String
            switch p.workspace {
            case .inPlace: mode = String(localized: "In place: agents change the files in this directory directly", bundle: .hermeshKit)
            case .worktree: mode = String(localized: "A separate git worktree for each task; the directory itself is left alone", bundle: .hermeshKit)
            case .unspecified: mode = String(localized: "Separate worktrees for git repositories; other directories in place", bundle: .hermeshKit)
            case .UNRECOGNIZED: throw Refusal.unsupported(type)
            }
            var lines = [String(localized: "Project: \(p.projectID)", bundle: .hermeshKit), String(localized: "Directory: \(p.path)", bundle: .hermeshKit),
                         String(localized: "Workspace: \(mode)", bundle: .hermeshKit)]
            if !p.defaultBaseRef.isEmpty { lines.append(String(localized: "Base branch: \(p.defaultBaseRef)", bundle: .hermeshKit)) }
            if !p.description_p.isEmpty { lines.append(String(localized: "Description: \(p.description_p)", bundle: .hermeshKit)) }
            return Card(type: type, title: String(localized: "Add project “\(p.projectID)” to \(target)", bundle: .hermeshKit), who: who, lines: lines,
                        why: String(localized: "Once approved, the machine registers the directory as a project, where personal agents with access can start tasks and read and write files.", bundle: .hermeshKit))
        case .removeProject(let p):
            return Card(type: type, title: String(localized: "Remove project “\(p.projectID)” from \(target)", bundle: .hermeshKit), who: who,
                        lines: [String(localized: "Project: \(p.projectID)", bundle: .hermeshKit)],
                        why: String(localized: "Once approved, agents can no longer start tasks in this project; the files in the directory are not deleted.", bundle: .hermeshKit))
        case .setPaused(let p):
            return p.paused
                ? Card(type: type, title: String(localized: "Pause \(target)", bundle: .hermeshKit), who: who, lines: [],
                       why: String(localized: "Once approved, the machine accepts no new tasks until you resume it, which you can do at any time.", bundle: .hermeshKit))
                : Card(type: type, title: String(localized: "Resume \(target)", bundle: .hermeshKit), who: who, lines: [],
                       why: String(localized: "Once approved, the machine accepts new tasks again.", bundle: .hermeshKit))
        case .setWorkspacePolicy(let p):
            return p.autoRelease
                ? Card(type: type, title: String(localized: "Release idle worktrees on \(target) automatically", bundle: .hermeshKit), who: who, lines: [],
                       why: String(localized: "Once approved, tasks’ worktrees idle for 7 days with no local changes are deleted there; their branches stay.", bundle: .hermeshKit))
                : Card(type: type, title: String(localized: "Stop releasing idle worktrees on \(target)", bundle: .hermeshKit), who: who, lines: [],
                       why: String(localized: "Once approved, worktrees stay until released by hand.", bundle: .hermeshKit))
        case .setName(let n):
            return Card(type: type, title: String(localized: "Rename \(target) to “\(n.name)”", bundle: .hermeshKit), who: who, lines: [],
                        why: String(localized: "The machine takes the new name; its projects and access stay the same.", bundle: .hermeshKit))
        case .maintainProvider(let op):
            let agent = codingAgentTitle(op.provider)
            let title: String
            switch op.action {
            case "install": title = String(localized: "Install \(agent) on \(target)", bundle: .hermeshKit)
            case "update": title = String(localized: "Update \(agent) on \(target)", bundle: .hermeshKit)
            case "verify": title = String(localized: "Verify \(agent) on \(target)", bundle: .hermeshKit)
            case "enable_auto": title = String(localized: "Enable automatic updates for \(agent) on \(target)", bundle: .hermeshKit)
            case "disable_auto": title = String(localized: "Disable automatic updates for \(agent) on \(target)", bundle: .hermeshKit)
            case "cancel": title = String(localized: "Cancel maintenance of \(agent) on \(target)", bundle: .hermeshKit)
            default: throw Refusal.unsupported(type)
            }
            return Card(type: type, title: title, who: who, lines: op.targetVersion.isEmpty ? [] : [op.targetVersion],
                        why: String(localized: "Hermesh runs this operation as the machine’s user.", bundle: .hermeshKit))
        case .setAgentDefaults(let d):
            let agent = codingAgentTitle(d.agent)
            let same = String(localized: "Same as \(agent)", bundle: .hermeshKit)
            var lines = [String(localized: "Model: \(d.model.isEmpty ? same : d.model)", bundle: .hermeshKit),
                         String(localized: "Reasoning effort: \(d.effort.isEmpty ? same : d.effort)", bundle: .hermeshKit)]
            switch d.speed {
            case .unspecified: if d.agent == "codex" { lines.append(String(localized: "Speed: \(same)", bundle: .hermeshKit)) }
            case .standard: lines.append(String(localized: "Speed: \(String(localized: "Standard", bundle: .hermeshKit))", bundle: .hermeshKit))
            case .fast: lines.append(String(localized: "Speed: \(String(localized: "Fast", bundle: .hermeshKit))", bundle: .hermeshKit))
            case .UNRECOGNIZED: throw Refusal.unsupported(type)
            }
            return Card(type: type, title: String(localized: "Set the model \(agent) runs with on \(target)", bundle: .hermeshKit), who: who, lines: lines,
                        why: String(localized: "Once approved, its tasks there that name no model run with these; the agent’s own configuration is not changed.", bundle: .hermeshKit))
        case .setHer(let h):
            if h.enabled && h.erase { throw Refusal.unsupported(type) }
            if h.erase {
                return Card(type: type, title: String(localized: "Turn Her off for \(target) and erase her data", bundle: .hermeshKit), who: who, lines: [],
                            why: String(localized: "Once approved, Her stops on the machine, and her assistants, conversations, memory, routines, files, connectors and model logins there are deleted. This can’t be undone.", bundle: .hermeshKit))
            }
            return h.enabled
                ? Card(type: type, title: String(localized: "Turn Her on for \(target)", bundle: .hermeshKit), who: who, lines: [],
                       why: String(localized: "Once approved, Her runs on the machine as its user and keeps her conversations, settings and logins there. Her assistants can read and write files, run commands and reach the internet on it; new ones ask before risky steps.", bundle: .hermeshKit))
                : Card(type: type, title: String(localized: "Turn Her off for \(target)", bundle: .hermeshKit), who: who, lines: [],
                       why: String(localized: "Once approved, Her stops on the machine; her conversations, settings and logins stay there.", bundle: .hermeshKit))
        case .setHerAssistantMode(let m):
            let mode: String
            switch m.permissionMode {
            case "manual": mode = String(localized: "Ask before each file change and command", bundle: .hermeshKit)
            case "accept-edits": mode = String(localized: "Change files without asking; ask before other commands", bundle: .hermeshKit)
            case "bypass-permissions": mode = String(localized: "Full access: ask before nothing", bundle: .hermeshKit)
            default: throw Refusal.unsupported(type)
            }
            let lines = [String(localized: "Assistant ID: \(m.botID)", bundle: .hermeshKit), String(localized: "Approval mode: \(mode)", bundle: .hermeshKit)]
            guard m.permissionMode == "bypass-permissions" else {
                return Card(type: type, title: String(localized: "Set how an assistant of Her acts on \(target)", bundle: .hermeshKit), who: who, lines: lines,
                            why: String(localized: "Once approved, the assistant works in this approval mode from its next step.", bundle: .hermeshKit))
            }
            return Card(type: type, title: String(localized: "Give an assistant of Her full access on \(target)", bundle: .hermeshKit), who: who, lines: lines,
                        why: String(localized: "Once approved, it runs any command and changes any file on the machine without asking, protected paths included; only deny rules and deleting a critical path still stop it.", bundle: .hermeshKit),
                        warning: String(localized: "What it reads, such as web pages and messages, can steer it. Approve only if you accept that.", bundle: .hermeshKit))
        case .setHerCommandConnector(let c):
            if c.remove {
                return Card(type: type, title: String(localized: "Remove Her’s connector “\(c.name)” from \(target)", bundle: .hermeshKit), who: who, lines: [],
                            why: String(localized: "Once approved, Her no longer starts it on the machine.", bundle: .hermeshKit))
            }
            let command = ([c.command] + c.args).map(quoted).joined(separator: " ")
            let env = c.env.keys.sorted().map { String(localized: "Environment: \(quoted($0))=\(quoted(c.env[$0] ?? ""))", bundle: .hermeshKit) }
            return Card(type: type, title: String(localized: "Let Her start connector “\(c.name)” on \(target)", bundle: .hermeshKit), who: who,
                        lines: [String(localized: "Command: \(command)", bundle: .hermeshKit)] + env,
                        why: String(localized: "Once approved, Her runs this command on the machine as its user whenever her assistants use the connector. It can do anything that user can: read and change files, run programs and reach the network.", bundle: .hermeshKit),
                        warning: String(localized: "Approve only if you know this command and trust where it comes from.", bundle: .hermeshKit))
        case .createFolder(let f):
            var lines = [String(localized: "In: \(f.parent.isEmpty ? "~" : f.parent)", bundle: .hermeshKit)]
            if f.gitInit { lines.append(String(localized: "A git repository with one empty commit", bundle: .hermeshKit)) }
            return Card(type: type, title: String(localized: "Create folder “\(f.name)” on \(target)", bundle: .hermeshKit), who: who, lines: lines,
                        why: String(localized: "Once approved, the machine makes the empty folder under its home folder, for a project.", bundle: .hermeshKit))
        case .setMaxRuntimeMode(let m):
            let mode: String
            switch m.mode {
            case .unspecified:
                return Card(type: type, title: String(localized: "Lift the permission limit of \(target)", bundle: .hermeshKit), who: who, lines: [],
                            why: String(localized: "Once approved, its threads run as their grants and you allow.", bundle: .hermeshKit))
            case .supervised: mode = String(localized: "Ask before each file change and command", bundle: .hermeshKit)
            case .autoEdits: mode = String(localized: "Change files without asking; ask before other commands", bundle: .hermeshKit)
            case .auto: mode = String(localized: "Change files and run routine checks without asking", bundle: .hermeshKit)
            case .fullAccess: mode = String(localized: "Full access: ask before nothing", bundle: .hermeshKit)
            case .UNRECOGNIZED: throw Refusal.unsupported(type)
            }
            return Card(type: type, title: String(localized: "Limit what threads may do on \(target)", bundle: .hermeshKit), who: who,
                        lines: [String(localized: "At most: \(mode)", bundle: .hermeshKit)],
                        why: String(localized: "Once approved, whoever starts them, its threads ask before anything beyond this.", bundle: .hermeshKit))
        case .none:
            throw Refusal.unsupported(type)
        }
    }

    /// s as a shell shows it: quoted, what doesn't show escaped, when it is
    /// empty or holds spaces, quotes or characters that don't show, so that
    /// what is shown is all there is.
    static func quoted(_ s: String) -> String {
        func unseen(_ c: Unicode.Scalar) -> Bool {
            switch c.properties.generalCategory {
            case .control, .format, .privateUse, .surrogate, .unassigned, .lineSeparator, .paragraphSeparator, .spaceSeparator: return true
            default: return false
            }
        }
        guard s.isEmpty || s.unicodeScalars.contains(where: { unseen($0) || "\"'\\".unicodeScalars.contains($0) }) else { return s }
        var out = "\""
        for c in s.unicodeScalars {
            switch c {
            case "\"": out += "\\\""
            case "\\": out += "\\\\"
            case "\n": out += "\\n"
            case "\t": out += "\\t"
            case "\r": out += "\\r"
            case " ": out += " "
            default: out += unseen(c) ? "\\u{\(String(c.value, radix: 16))}" : String(c)
            }
        }
        return out + "\""
    }

    /// The two digits to show after signing root cert r, when it adds a
    /// device (or when the devices could not be listed), with that device's
    /// name if known; or after signing the machine cert of a machine that
    /// asked for them (a Mac joining as a machine only).
    public static func joinMatchCode(_ r: SigningRequest, _ hints: Hints) -> (label: String?, digits: String, machine: Bool)? {
        if r.type == machineCert { return r.showMatch ? (nil, joinMatch(r.body), true) : nil }
        guard r.type == rootCert, let c = try? strict(r, Hermesh_Identity_V1_RootCert.self), c.version > 1 else { return nil }
        guard let devices = hints.devices else { return (nil, joinMatch(r.body), false) }
        guard let added = c.rootKeys.first(where: { devices[keyID($0.publicKey)] == nil }) else { return nil }
        return (added.label, joinMatch(r.body), false)
    }

    /// The Touch ID prompt: short, says what is being approved, from the
    /// cards (so from the signed bytes).
    public static func reason(_ cards: [Card], caller: CallerInfo, reissue: Bool, firstSetup: Bool = false) -> String {
        var first = cards[0].title
        if let code = cards[0].code { first = String(localized: "\(first) (verification code \(code))", bundle: .hermeshKit) }
        let text = firstSetup ? String(localized: "Create your owner key", bundle: .hermeshKit)
            : reissue ? String(localized: "\(first), and reissue the credentials it signed (\(cards.count - 1))", bundle: .hermeshKit)
            : cards.count == 1 ? first
            : String(localized: "\(first) (1 of \(cards.count))", bundle: .hermeshKit, comment: "The first of several requests in a batch")
        guard let program = caller.program else { return text }
        let name = program.isEmpty ? String(localized: "Unknown program", bundle: .hermeshKit) : program
        return String(localized: "\(name) requests: \(text)", bundle: .hermeshKit)
    }

    private static func machineLabel(_ id: String, _ hints: Hints) -> String {
        hints.machineNames[id].map { String(localized: "machine “\($0)”", bundle: .hermeshKit, comment: "Used within a sentence") }
            ?? String(localized: "machine \(String(id.prefix(12)))…", bundle: .hermeshKit, comment: "Used within a sentence; %@ is the start of a machine id")
    }

    private static func machineScope(_ ids: [String], _ hints: Hints) -> String {
        scope(ids.map { $0 == "*" ? $0 : hints.machineNames[$0] ?? String($0.prefix(12)) })
    }

    public static func scope(_ ids: [String]) -> String { isAll(ids) ? String(localized: "All", bundle: .hermeshKit) : list(ids) }

    /// A scope of ids that names everything.
    public static func isAll(_ ids: [String]) -> Bool { ids.contains("*") || ids.isEmpty }

    /// Items joined the way this language lists them.
    public static func list(_ items: [String]) -> String {
        items.joined(separator: String(localized: ", ", bundle: .hermeshKit, comment: "Separates the items of a list"))
    }

    /// Matches identity.VerificationCode: binds the requester's key to this
    /// owner. The signer computes it from the bytes it signs, so neither the
    /// center nor hermeshd can show a different one.
    public static func verificationCode(_ kind: PairingKind, _ spki: Data, _ ownerID: String) -> String {
        var d = Data("hermesh-enroll-v1\0".utf8)
        d.append(UInt8(kind.rawValue))
        d.append(spki)
        d.append(Data(ownerID.utf8))
        let c = Array(Base32.encode(sha256(d), alphabet: Base32.crockford).prefix(12))
        return "\(String(c[0..<4]))-\(String(c[4..<8]))-\(String(c[8..<12]))"
    }

    /// Matches identity.JoinMatchCode: the two digits to type on a device
    /// that joined, from the cert admitting it (the root cert listing it, or
    /// a machine's cert).
    public static func joinMatch(_ certBody: Data) -> String {
        var d = Data("hermesh-join-match-v1\0".utf8)
        d.append(certBody)
        let h = sha256(d)
        return String(format: "%02d", (Int(h[0]) << 8 | Int(h[1])) % 100)
    }

    /// Matches identity.Fingerprint: lowercase base32 of SHA-256(SPKI).
    public static func keyID(_ spki: Data) -> String { Base32.encode(sha256(spki)).lowercased() }

    /// Matches identity.ShortFingerprint: base32(sha256(spki))[:8], split.
    public static func fingerprint(_ spki: Data) -> String {
        let b32 = keyID(spki)
        return "\(b32.prefix(4)) \(b32.dropFirst(4).prefix(4))"
    }

    public static func sha256(_ d: Data) -> Data { Data(SHA256.hash(data: d)) }
}

/// Coding agents by the id a grant names.
public func codingAgentTitle(_ name: String) -> String {
    switch name {
    case "claude": return "Claude Code"
    case "codex": return "Codex"
    case "gemini": return "Gemini CLI"
    case "antigravity": return "Antigravity"
    case "cursor": return "Cursor"
    case "copilot": return "Copilot CLI"
    case "opencode": return "OpenCode"
    case "qwen": return "Qwen Code"
    case "goose": return "Goose"
    case "auggie": return "Augment"
    case "kimi": return "Kimi"
    case "kiro": return "Kiro"
    case "qoder": return "Qoder"
    case "deepseek": return "DeepSeek Harness"
    case "codebuddy": return "CodeBuddy Code"
    case "grok": return "Grok"
    default: return name
    }
}

public enum Base32 {
    public static let standard = Array("ABCDEFGHIJKLMNOPQRSTUVWXYZ234567")
    public static let crockford = Array("0123456789ABCDEFGHJKMNPQRSTVWXYZ")
    public static func encode(_ data: Data, alphabet: [Character] = standard) -> String {
        var out = ""
        var buffer = 0, bits = 0
        for b in data {
            buffer = (buffer << 8) | Int(b)
            bits += 8
            while bits >= 5 {
                out.append(alphabet[(buffer >> (bits - 5)) & 31])
                bits -= 5
            }
        }
        if bits > 0 { out.append(alphabet[(buffer << (5 - bits)) & 31]) }
        return out
    }
}
