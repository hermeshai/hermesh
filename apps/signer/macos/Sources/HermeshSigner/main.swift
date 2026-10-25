import AppKit
import Foundation

// Hermesh Signer: a faceless app that holds the owner root key. It serves
// only hermeshd's signing stream, and shows a panel when there is
// something to sign.

signal(SIGPIPE, SIG_IGN)
signal(SIGUSR1, SIG_IGN)

let options: Options
do {
    options = try Options.parse(Array(CommandLine.arguments.dropFirst()))
} catch {
    log("\(error.localizedDescription)\n\(Options.usage)")
    exit(2)
}

// Exit with the app that started the signer.
var parentWatch: DispatchSourceProcess?
if let pid = options.parentPID {
    let source = DispatchSource.makeProcessSource(identifier: pid, eventMask: .exit, queue: .main)
    source.setEventHandler { exit(0) }
    source.resume()
    parentWatch = source
    if kill(pid, 0) != 0, errno == ESRCH { exit(0) } // already gone
}

let store: RootKeyStore
if let path = options.testKey {
    do {
        store = try TestKeyFile(path: path)
    } catch {
        log("test key \(path): \(error.localizedDescription)")
        exit(1)
    }
    log("TEST MODE: software root key in \(path), no Touch ID")
} else {
    let key = SecureEnclaveKey()
    key.adoptLegacyBlob()
    log("root key: \(key.location)")
    store = key
}

MainActor.assumeIsolated {
    let app = NSApplication.shared
    app.setActivationPolicy(.accessory)
    let signer = Signer(options: options, store: store)
    signer.start()
    let activation = DispatchSource.makeSignalSource(signal: SIGUSR1, queue: .main)
    activation.setEventHandler { MainActor.assumeIsolated { signer.activate() } }
    activation.resume()
    withExtendedLifetime(activation) { app.run() }
}
