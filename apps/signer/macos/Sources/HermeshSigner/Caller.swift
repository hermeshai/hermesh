import Foundation
import HermeshKit
import HermeshProto

/// Who started a signing request (CallerInfo, in HermeshKit), as the Mac
/// signer tells it: by where the calling program lives.
extension CallerInfo {
    static let warning = String(localized: "Hermesh didn’t start this. It may come from the command line or another program on this Mac. If you didn’t start it, deny it.", bundle: .module)

    /// The signer lives at <X>.app/Contents/Helpers/Hermesh Signer.app; a
    /// caller is Hermesh when its executable is one of <X>.app/Contents/MacOS.
    static func of(_ caller: Hermesh_Local_V1_Caller?, signerBundle: URL) -> CallerInfo {
        guard let exe = caller?.executable, !exe.isEmpty else { return CallerInfo(program: "") }
        let url = URL(fileURLWithPath: exe).resolvingSymlinksInPath()
        if let app = hostApp(signerBundle.resolvingSymlinksInPath()),
           url.deletingLastPathComponent().standardizedFileURL.path == app.appendingPathComponent("Contents/MacOS").standardizedFileURL.path {
            return CallerInfo(program: nil)
        }
        return CallerInfo(program: url.lastPathComponent, path: url.path)
    }

    /// One caller for a batch: any caller that is not Hermesh wins.
    static func of(_ reqs: [SigningRequest], signerBundle: URL) -> CallerInfo {
        let all = reqs.map { of($0.hasCaller ? $0.caller : nil, signerBundle: signerBundle) }
        return all.first { !$0.isHermesh } ?? all.first ?? CallerInfo(program: "")
    }

    private static func hostApp(_ signer: URL) -> URL? {
        let helpers = signer.deletingLastPathComponent()
        let contents = helpers.deletingLastPathComponent()
        let app = contents.deletingLastPathComponent()
        guard signer.pathExtension == "app", helpers.lastPathComponent == "Helpers",
              contents.lastPathComponent == "Contents", app.pathExtension == "app" else { return nil }
        return app
    }
}
