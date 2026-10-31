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
        case nil: return String(localized: "Requested by the Hermesh app", bundle: .module)
        case "": return String(localized: "Requested by an unknown program", bundle: .module)
        case let p?: return String(localized: "Requested by \(p)", bundle: .module, comment: "%@ is a program's file name")
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
            case .project: return String(localized: "Projects", bundle: .module)
            case .machine: return String(localized: "Machines", bundle: .module)
            case .codingAgent: return String(localized: "Coding agents", bundle: .module)
            case .expires: return String(localized: "Expires", bundle: .module)
            case .device: return String(localized: "Device", bundle: .module)
            case .trustedDevices: return String(localized: "Trusted devices", bundle: .module)
            case .extra: return String(localized: "Also", bundle: .module, comment: "Label of a card row with a limit beyond the defaults")
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
        case .messageMismatch(let t): return String(localized: "What would be signed doesn’t match what would be shown (\(t)).", bundle: .module, comment: "%@ is a request type")
        case .unknownType(let t): return String(localized: "Hermesh doesn’t recognize this kind of request (\(t)).", bundle: .module)
        case .unparseable(let t): return String(localized: "The request can’t be parsed (\(t)).", bundle: .module)
        case .unknownFields(let t): return String(localized: "The request contains something Hermesh doesn’t recognize (\(t)). Hermesh may need an update.", bundle: .module)
        case .unsupported(let t): return String(localized: "Hermesh can’t show this request in full (\(t)).", bundle: .module)
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
        on(String(localized: "Creates your owner key in this Mac’s Secure Enclave. Without it, no one can use the services on your computers, and only you can approve, with Touch ID.", bundle: .module),
           String(localized: "Creates your owner key in this iPhone’s Secure Enclave. Without it, no one can use the services on your computers, and only you can approve, with Face ID.", bundle: .module))
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
    /// produce) is refused. hermeshd marshals deterministically, and the
    /// signed types have no maps.
    public static func strict<M: SwiftProtobuf.Message>(_ r: SigningRequest, _: M.Type) throws -> M {
        var options = BinaryDecodingOptions()
        options.discardUnknownFields = true
        let m: M
        do {
            m = try M(serializedBytes: r.body, options: options)
        } catch {
            throw Refusal.unparseable(r.type)
        }
        guard let again: Data = try? m.serializedBytes(), again == r.body else { throw Refusal.unknownFields(r.type) }
        return m
    }

    public static func card(_ r: SigningRequest, _ hints: Hints = .init(), reissue: Bool = false) throws -> Card {
        guard r.message == signingInput(type: r.type, body: r.body) else { throw Refusal.messageMismatch(r.type) }
        switch r.type {
        case ownerDeviceCert:
            let m = try strict(r, Hermesh_Identity_V1_OwnerDeviceCert.self)
            return Card(type: r.type, title: on(String(localized: "Issue this Mac a certificate for end-to-end connections", bundle: .module),
                           String(localized: "Issue this iPhone a certificate for end-to-end connections", bundle: .module)), who: "", lines: [],
                        why: on(String(localized: "Once approved, Hermesh on this Mac uses it for end-to-end encrypted connections to your machines, to view and manage your sessions and machines.", bundle: .module),
                           String(localized: "Once approved, Hermesh on this iPhone uses it for end-to-end encrypted connections to your machines, to view and manage your sessions and machines.", bundle: .module)),
                        ids: [String(localized: "Device name: \(m.label)", bundle: .module), String(localized: "Fingerprint: \(fingerprint(m.devicePublicKey))", bundle: .module)])
        case rootCert:
            var c = describeRoot(try strict(r, Hermesh_Identity_V1_RootCert.self), hints)
            // A device found by the code it shows: that code binds its key.
            if r.confirmed { c.code = nil }
            return c
        case pairingRequest:
            let p = try strict(r, Hermesh_Identity_V1_PairingRequest.self)
            guard p.kind == .device else { throw Refusal.unsupported(r.type) }
            return Card(type: r.type, title: on(String(localized: "Add this Mac to your Hermesh", bundle: .module),
                           String(localized: "Add this iPhone to your Hermesh", bundle: .module)), who: "",
                        code: verificationCode(.device, p.subjectPublicKey, p.ownerID), lines: [],
                        why: on(String(localized: "This sends a join request to your existing devices. Check the same code on one of them and approve, and this Mac becomes a trusted device.", bundle: .module),
                           String(localized: "This sends a join request to your existing devices. Check the same code on one of them and approve, and this iPhone becomes a trusted device.", bundle: .module)),
                        ids: [String(localized: "Device name: \(p.label)", bundle: .module), String(localized: "Owner identity ID: \(p.ownerID)", bundle: .module)])
        case machineCert:
            var c = describeMachine(try strict(r, Hermesh_Identity_V1_MachineCert.self), hints, reissue: reissue)
            // Found by the code its terminal shows: that code binds its key,
            // so there is nothing to compare.
            if r.confirmed { c.code = nil }
            return c
        case grant:
            let g = try strict(r, Hermesh_Identity_V1_Grant.self)
            if case .UNRECOGNIZED = g.taskVisibility { throw Refusal.unsupported(r.type) }
            var c = describeGrant(g, hints, reissue: reissue)
            // The person already said, signed in to their account, that this
            // agent is theirs: its side shows no code to compare.
            if r.confirmed { c.code = nil }
            return c
        case adminCommand:
            return try describeAdmin(try strict(r, Hermesh_Identity_V1_AdminCommand.self), hints)
        case eraseOrder:
            let e = try strict(r, Hermesh_Identity_V1_EraseOrder.self)
            return Card(type: r.type, title: String(localized: "Erase everything in your Hermesh", bundle: .module), who: "", lines: [],
                        why: on(String(localized: "Once approved, Hermesh on all your trusted devices and machines, this Mac included, erases its keys, sessions and settings, and your agents are disconnected. This can’t be undone.", bundle: .module),
                           String(localized: "Once approved, Hermesh on all your trusted devices and machines, this iPhone included, erases its keys, sessions and settings, and your agents are disconnected. This can’t be undone.", bundle: .module)),
                        warning: String(localized: "Approve only if you started erasing everything yourself.", bundle: .module),
                        ids: [String(localized: "Owner identity ID: \(e.ownerID)", bundle: .module), String(localized: "Erase ID: \(e.eraseID)", bundle: .module)])
        default:
            throw Refusal.unknownType(r.type)
        }
    }

    /// The owner's trusted devices change: compared with the devices
    /// hermeshd lists, by the key ids computed from the signed bytes.
    private static func describeRoot(_ c: Hermesh_Identity_V1_RootCert, _ hints: Hints) -> Card {
        let type = rootCert
        let keys = c.rootKeys.map { (id: keyID($0.publicKey), key: $0) }
        let names = keys.map { $0.key.label.isEmpty ? String(localized: "Unnamed device", bundle: .module) : $0.key.label }
        if c.version == 1 {
            return Card(type: type, title: String(localized: "Create your owner identity", bundle: .module), who: "", lines: [],
                        why: on(String(localized: "Once approved, this Mac becomes your first trusted device; any device that joins later must be approved by a trusted device.", bundle: .module),
                           String(localized: "Once approved, this iPhone becomes your first trusted device; any device that joins later must be approved by a trusted device.", bundle: .module)),
                        ids: [String(localized: "Owner identity ID: \(c.ownerID)", bundle: .module), String(localized: "Device name: \(names.first ?? "")", bundle: .module)])
        }
        let lines = [String(localized: "Your trusted devices after this: \(list(names))", bundle: .module), String(localized: "Version \(Int(c.version))", bundle: .module)]
        guard let devices = hints.devices else {
            return Card(type: type, title: String(localized: "Update your list of trusted devices", bundle: .module), who: String(localized: "Your trusted devices", bundle: .module), lines: lines,
                        why: String(localized: "Once approved, the list above becomes your trusted devices.", bundle: .module))
        }
        let added = keys.filter { devices[$0.id] == nil }
        let removed = devices.filter { d in !keys.contains { $0.id == d.key } }.sorted { $0.key < $1.key }
        if let a = added.first {
            return Card(type: type, title: String(localized: "Add “\(a.key.label)” as a trusted device", bundle: .module), who: "",
                        code: verificationCode(.device, a.key.publicKey, c.ownerID), lines: lines,
                        rows: [CardRow(kind: .device, value: a.key.label),
                               CardRow(kind: .trustedDevices, value: String(localized: "\(keys.count) in all once it joins", bundle: .module, comment: "The number of trusted devices"))],
                        why: on(String(localized: "Uses your owner key to make it a trusted device too, able to approve agents, machines and new devices just like this Mac.", bundle: .module),
                           String(localized: "Uses your owner key to make it a trusted device too, able to approve agents, machines and new devices just like this iPhone.", bundle: .module)),
                        warning: String(localized: "Approve only if that device is in your hands.", bundle: .module))
        }
        if let r = removed.first {
            return Card(type: type, title: String(localized: "Remove trusted device “\(r.value)”", bundle: .module), who: String(localized: "Device “\(r.value)”", bundle: .module), lines: lines,
                        why: on(String(localized: "Once approved, it can no longer approve anything, and its own machine is removed with it; this Mac reissues the machine and personal agent credentials it signed.", bundle: .module),
                           String(localized: "Once approved, it can no longer approve anything, and its own machine is removed with it; this iPhone reissues the machine and personal agent credentials it signed.", bundle: .module)))
        }
        return Card(type: type, title: String(localized: "Update your list of trusted devices", bundle: .module), who: String(localized: "Your trusted devices", bundle: .module), lines: lines,
                        why: String(localized: "Once approved, the list above becomes your trusted devices.", bundle: .module))
    }

    /// A machine cert is the machine's key: its name is a setting, shown
    /// here from hermeshd's list when the machine is already known.
    private static func describeMachine(_ m: Hermesh_Identity_V1_MachineCert, _ hints: Hints, reissue: Bool) -> Card {
        let type = machineCert
        let id = String(localized: "Machine ID: \(m.machineID)", bundle: .module)
        if let name = hints.machineNames[m.machineID] {
            return Card(type: type, title: String(localized: "Reissue the certificate of machine “\(name)”", bundle: .module), who: String(localized: "Machine “\(name)” (already yours)", bundle: .module),
                        lines: [id], why: reissue ? on(String(localized: "Once approved, this machine stays yours, unchanged, now signed by this Mac.", bundle: .module),
                           String(localized: "Once approved, this machine stays yours, unchanged, now signed by this iPhone.", bundle: .module))
                            : String(localized: "Once approved, this machine stays yours.", bundle: .module))
        }
        if !m.deviceKeyID.isEmpty {
            return Card(type: type, title: String(localized: "Share the agents on this Mac", bundle: .module), who: "", lines: [],
                        why: String(localized: "Once approved, trusted clients you grant access can use and control the agents on this Mac, over end-to-end encrypted connections. You can turn this off in Settings at any time.", bundle: .module),
                        ids: [id, String(localized: "Fingerprint: \(fingerprint(m.machinePublicKey))", bundle: .module)])
        }
        return Card(type: type, title: String(localized: "Approve a new machine", bundle: .module), who: String(localized: "Machine ID \(String(m.machineID.prefix(12)))…", bundle: .module),
                    code: verificationCode(.machine, m.machinePublicKey, m.ownerID),
                    lines: [id, String(localized: "Fingerprint: \(fingerprint(m.machinePublicKey))", bundle: .module)],
                    why: String(localized: "Once approved, this machine becomes yours, and agents you grant access can start sessions and run commands on it. Approve only if you know this machine; you can remove it later from the machine list.", bundle: .module),
                    warning: String(localized: "If the verification code doesn’t match, this isn’t the machine in front of you.", bundle: .module))
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
        let fullWarning = String(localized: "Full trust: on the chosen machines, this personal agent can do anything you can, including reading credentials and accessing the network.", bundle: .module)
        let id = String(localized: "Fingerprint: \(fingerprint(g.clientPublicKey))", bundle: .module)
        // Limits beyond the defaults are part of the scope too.
        var limits: [String] = []
        if g.taskVisibility == .all { limits.append(String(localized: "Can also see other personal agents’ sessions in these projects", bundle: .module)) }
        if g.maxConcurrentSessions > 0 { limits.append(String(localized: "Sessions at once: up to \(Int(g.maxConcurrentSessions))", bundle: .module)) }
        var rows = [CardRow(kind: .project, value: projects), CardRow(kind: .codingAgent, value: agents),
                    CardRow(kind: .expires, value: String(localized: "Until \(day)", bundle: .module))]
        if !isAll(g.machineIds) { rows.insert(CardRow(kind: .machine, value: machines), at: 1) }
        rows += limits.map { CardRow(kind: .extra, value: $0) }
        // An MCP connection: a server holds this agent's key for a remote app.
        let custodian = URL(string: g.custodian)?.host ?? g.custodian
        var custody: [String] = []
        if !custodian.isEmpty {
            custody = [String(localized: "Connects through \(custodian), which holds its key", bundle: .module)]
            rows.append(CardRow(kind: .extra, value: custody[0]))
        }
        if g.version <= 1 {
            return Card(type: type, title: String(localized: "Approve “\(name)” to connect to your computer", bundle: .module), who: "",
                        code: verificationCode(.client, g.clientPublicKey, g.ownerID),
                        lines: [String(localized: "Personal agent “\(name)” (\(id))", bundle: .module), String(localized: "Projects it can use: \(projects)", bundle: .module),
                                String(localized: "On these machines: \(machines)", bundle: .module), String(localized: "Coding agents it can run: \(agents)", bundle: .module)]
                            + limits + custody + [String(localized: "Expires: \(when)", bundle: .module), fullWarning],
                        rows: rows,
                        why: custodian.isEmpty
                            ? String(localized: "Uses your owner key to issue it an end-to-end connection grant, which you can revoke at any time.", bundle: .module)
                            : String(localized: "Uses your owner key to issue it a connection grant, which you can revoke at any time. \(custodian) holds this agent’s key: it can act as the agent within this access, and sees the sessions it reaches.", bundle: .module),
                        warning: String(localized: "Full trust: in the chosen projects, it can do anything you can.", bundle: .module))
        }
        let cur = hints.clients[g.grantID]
        let was = cur.map { (projects: scope($0.spec.projectIds), machines: machineScope($0.spec.machineIds, hints), agents: scope($0.spec.agents.map(codingAgentTitle))) }
        if reissue, let was, was.projects == projects, was.machines == machines, was.agents == agents {
            return Card(type: type, title: String(localized: "Reissue the grant of “\(name)”", bundle: .module), who: String(localized: "Personal agent “\(name)” (\(id))", bundle: .module),
                        lines: [String(localized: "Same access: \(projects); \(agents); expires \(when)", bundle: .module)] + limits + [String(localized: "Version \(Int(g.version))", bundle: .module)],
                        why: on(String(localized: "Once approved, it keeps the same access, now signed by this Mac; the old version stops working.", bundle: .module),
                           String(localized: "Once approved, it keeps the same access, now signed by this iPhone; the old version stops working.", bundle: .module)))
        }
        var lines: [String] = []
        if let cur, let was {
            if was.projects != projects { lines.append(String(localized: "Projects: \(was.projects) → \(projects)", bundle: .module)) }
            if was.machines != machines { lines.append(String(localized: "Machines: \(was.machines) → \(machines)", bundle: .module)) }
            if was.agents != agents { lines.append(String(localized: "Coding agents: \(was.agents) → \(agents)", bundle: .module)) }
            lines += limits
            let before = cur.expiresAt.date.formatted(date: .abbreviated, time: .shortened)
            lines.append(String(localized: "Expires: \(when) (before the change: \(before))", bundle: .module))
            if lines.count == 1 { lines.insert(String(localized: "Projects, machines and coding agents stay the same; only the expiry changes.", bundle: .module), at: 0) }
            lines.append(String(localized: "(What applies before the change comes from Hermesh’s current list; what applies after it comes from what will be signed.)", bundle: .module))
        } else {
            lines = [String(localized: "New settings:", bundle: .module), String(localized: "Projects: \(projects)", bundle: .module),
                     String(localized: "Machines: \(machines)", bundle: .module), String(localized: "Coding agents: \(agents)", bundle: .module)]
                + limits + [String(localized: "Expires: \(when)", bundle: .module),
                            String(localized: "(Hermesh has no record of its settings before the change, so they can’t be compared.)", bundle: .module)]
        }
        return Card(type: type, title: String(localized: "Change the access of personal agent “\(name)”", bundle: .module),
                    who: String(localized: "Personal agent “\(name)” (\(id))", bundle: .module), lines: lines,
                    why: String(localized: "Once approved, the new settings take effect at once (version \(Int(g.version))) and the old ones stop working; from then on it can act only within the new settings.", bundle: .module),
                    warning: fullWarning)
    }

    private static func describeAdmin(_ c: Hermesh_Identity_V1_AdminCommand, _ hints: Hints) throws -> Card {
        let type = adminCommand
        let target = machineLabel(c.targetMachineID, hints)
        let who = String(localized: "Your owner key, instructing \(target)", bundle: .module)
        switch c.command {
        case .addProject(let p):
            let mode: String
            switch p.workspace {
            case .inPlace: mode = String(localized: "In place: agents change the files in this directory directly", bundle: .module)
            case .worktree: mode = String(localized: "A separate git worktree for each session; the directory itself is left alone", bundle: .module)
            case .unspecified: mode = String(localized: "Separate worktrees for git repositories; other directories in place", bundle: .module)
            case .UNRECOGNIZED: throw Refusal.unsupported(type)
            }
            var lines = [String(localized: "Project: \(p.projectID)", bundle: .module), String(localized: "Directory: \(p.path)", bundle: .module),
                         String(localized: "Workspace: \(mode)", bundle: .module)]
            if !p.defaultBaseRef.isEmpty { lines.append(String(localized: "Base branch: \(p.defaultBaseRef)", bundle: .module)) }
            if !p.description_p.isEmpty { lines.append(String(localized: "Description: \(p.description_p)", bundle: .module)) }
            lines.append(String(localized: "Available coding agents: \(scope(p.agents.map(codingAgentTitle)))", bundle: .module))
            return Card(type: type, title: String(localized: "Add project “\(p.projectID)” to \(target)", bundle: .module), who: who, lines: lines,
                        why: String(localized: "Once approved, the machine registers the directory as a project, where personal agents with access can start sessions and read and write files.", bundle: .module))
        case .removeProject(let p):
            return Card(type: type, title: String(localized: "Remove project “\(p.projectID)” from \(target)", bundle: .module), who: who,
                        lines: [String(localized: "Project: \(p.projectID)", bundle: .module)],
                        why: String(localized: "Once approved, agents can no longer start sessions in this project; the files in the directory are not deleted.", bundle: .module))
        case .setPaused(let p):
            return p.paused
                ? Card(type: type, title: String(localized: "Pause \(target)", bundle: .module), who: who, lines: [],
                       why: String(localized: "Once approved, the machine accepts no new sessions until you resume it, which you can do at any time.", bundle: .module))
                : Card(type: type, title: String(localized: "Resume \(target)", bundle: .module), who: who, lines: [],
                       why: String(localized: "Once approved, the machine accepts new sessions again.", bundle: .module))
        case .setName(let n):
            return Card(type: type, title: String(localized: "Rename \(target) to “\(n.name)”", bundle: .module), who: who, lines: [],
                        why: String(localized: "The machine takes the new name; its projects and access stay the same.", bundle: .module))
        case .setAgentDefaults(let d):
            let agent = codingAgentTitle(d.agent)
            let same = String(localized: "Same as \(agent)", bundle: .module)
            var lines = [String(localized: "Model: \(d.model.isEmpty ? same : d.model)", bundle: .module),
                         String(localized: "Reasoning effort: \(d.effort.isEmpty ? same : d.effort)", bundle: .module)]
            switch d.speed {
            case .unspecified: if d.agent == "codex" { lines.append(String(localized: "Speed: \(same)", bundle: .module)) }
            case .standard: lines.append(String(localized: "Speed: \(String(localized: "Standard", bundle: .module))", bundle: .module))
            case .fast: lines.append(String(localized: "Speed: \(String(localized: "Fast", bundle: .module))", bundle: .module))
            case .UNRECOGNIZED: throw Refusal.unsupported(type)
            }
            return Card(type: type, title: String(localized: "Set the model \(agent) runs with on \(target)", bundle: .module), who: who, lines: lines,
                        why: String(localized: "Once approved, its sessions there that name no model run with these; the agent’s own configuration is not changed.", bundle: .module))
        case .none:
            throw Refusal.unsupported(type)
        }
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
        if let code = cards[0].code { first = String(localized: "\(first) (verification code \(code))", bundle: .module) }
        let text = firstSetup ? String(localized: "Create your owner key", bundle: .module)
            : reissue ? String(localized: "\(first), and reissue the credentials it signed (\(cards.count - 1))", bundle: .module)
            : cards.count == 1 ? first
            : String(localized: "\(first) (1 of \(cards.count))", bundle: .module, comment: "The first of several requests in a batch")
        guard let program = caller.program else { return text }
        let name = program.isEmpty ? String(localized: "Unknown program", bundle: .module) : program
        return String(localized: "\(name) requests: \(text)", bundle: .module)
    }

    private static func machineLabel(_ id: String, _ hints: Hints) -> String {
        hints.machineNames[id].map { String(localized: "machine “\($0)”", bundle: .module, comment: "Used within a sentence") }
            ?? String(localized: "machine \(String(id.prefix(12)))…", bundle: .module, comment: "Used within a sentence; %@ is the start of a machine id")
    }

    private static func machineScope(_ ids: [String], _ hints: Hints) -> String {
        scope(ids.map { $0 == "*" ? $0 : hints.machineNames[$0] ?? String($0.prefix(12)) })
    }

    public static func scope(_ ids: [String]) -> String { isAll(ids) ? String(localized: "All", bundle: .module) : list(ids) }

    /// A scope of ids that names everything.
    public static func isAll(_ ids: [String]) -> Bool { ids.contains("*") || ids.isEmpty }

    /// Items joined the way this language lists them.
    public static func list(_ items: [String]) -> String {
        items.joined(separator: String(localized: ", ", bundle: .module, comment: "Separates the items of a list"))
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
