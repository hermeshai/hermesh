import Foundation

/// Command-line options. The test options exist for end-to-end tests only:
/// none of them is on unless given, and they need --test-key, so a normal
/// launch always uses the Secure Enclave and Touch ID.
struct Options: Equatable {
    enum Decision: String { case approve, reject }

    var socket = Paths.socket
    /// Exit when this process exits (the app that started the signer).
    var parentPID: pid_t?
    /// A software key in this file instead of the Secure Enclave, and no
    /// LocalAuthentication.
    var testKey: String?
    /// Decide every confirmation automatically once its content is built.
    var testDecision: Decision?
    /// Append one JSON line per decision, with what the panel showed.
    var testReport: String?

    static let usage = "usage: HermeshSigner [--socket PATH] [--parent-pid PID] [--test-key FILE [--test-decision approve|reject] [--test-report FILE]]"

    static func parse(_ args: [String]) throws -> Options {
        var o = Options()
        var rest = args[...]
        func value(_ flag: String) throws -> String {
            guard let v = rest.popFirst() else { throw SignerError(message: "\(flag) needs a value") }
            return v
        }
        while let flag = rest.popFirst() {
            switch flag {
            case "--socket": o.socket = try value(flag)
            case "--parent-pid":
                guard let pid = pid_t(try value(flag)), pid > 0 else { throw SignerError(message: "--parent-pid needs a process id") }
                o.parentPID = pid
            case "--test-key": o.testKey = try value(flag)
            case "--test-decision":
                guard let d = Decision(rawValue: try value(flag)) else { throw SignerError(message: "--test-decision is approve or reject") }
                o.testDecision = d
            case "--test-report": o.testReport = try value(flag)
            // Foundation reads the language from the command line itself.
            case "-AppleLanguages": _ = try value(flag)
            default: throw SignerError(message: "unknown argument \(flag)")
            }
        }
        if o.testKey == nil, o.testDecision != nil || o.testReport != nil {
            throw SignerError(message: "--test-decision and --test-report need --test-key")
        }
        return o
    }
}
