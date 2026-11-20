import Foundation

public enum HostedAccountDeletion {
    public static func browserURL(consoleURL: String, enabledOrigins: String) -> URL? {
        guard var url = URLComponents(string: consoleURL), url.scheme?.lowercased() == "https",
              let host = url.host, !host.isEmpty, url.user == nil, url.password == nil else { return nil }
        url.scheme = "https"
        url.host = host.lowercased()
        if url.port == 443 { url.port = nil }
        url.path = ""
        url.query = nil
        url.fragment = nil
        guard let origin = url.string, enabledOrigins.split(whereSeparator: \.isWhitespace).contains(Substring(origin)) else { return nil }
        url.path = "/account/delete"
        return url.url
    }
}
