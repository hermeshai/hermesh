import HermeshKit
import Testing

@Test func hostedAccountDeletionUsesOnlyEnabledConsoleOrigins() {
    let origins = "https://hermesh.io https://alternate.example:8443"
    #expect(HostedAccountDeletion.browserURL(consoleURL: "https://HERMESH.io:443/overview?code=fixture#old", enabledOrigins: origins)?.absoluteString
        == "https://hermesh.io/account/delete")
    #expect(HostedAccountDeletion.browserURL(consoleURL: "https://alternate.example:8443/settings", enabledOrigins: origins)?.absoluteString
        == "https://alternate.example:8443/account/delete")
    #expect(HostedAccountDeletion.browserURL(consoleURL: "https://hermesh.io", enabledOrigins: "") == nil)
    for console in ["", "/overview", "http://hermesh.io", "https://hermesh.io.evil.example",
                    "https://hermesh.io:444", "https://fixture@hermesh.io", "https://alternate.example"] {
        #expect(HostedAccountDeletion.browserURL(consoleURL: console, enabledOrigins: origins) == nil, "\(console)")
    }
}
