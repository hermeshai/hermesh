import Foundation

/// her's JSON as it comes (her/crates/her-protocol/schema.json). Her's
/// protocol is read by its fields: what a newer her adds passes by, and what
/// this side does not know is left alone.
public enum JSON: Hashable, Sendable {
    case null
    case bool(Bool)
    case number(Double)
    case string(String)
    case array([JSON])
    case object([String: JSON])

    /// JSON from its bytes; empty bytes are null.
    public static func parse(_ data: Data) throws -> JSON {
        if data.isEmpty { return .null }
        return JSON(any: try JSONSerialization.jsonObject(with: data, options: [.fragmentsAllowed]))
    }

    /// Its bytes.
    public func data() -> Data {
        (try? JSONSerialization.data(withJSONObject: any, options: [.fragmentsAllowed])) ?? Data("null".utf8)
    }

    private init(any v: Any) {
        switch v {
        case let n as NSNumber:
            self = CFGetTypeID(n) == CFBooleanGetTypeID() ? .bool(n.boolValue) : .number(n.doubleValue)
        case let s as String: self = .string(s)
        case let a as [Any]: self = .array(a.map(JSON.init(any:)))
        case let o as [String: Any]: self = .object(o.mapValues(JSON.init(any:)))
        default: self = .null
        }
    }

    private var any: Any {
        switch self {
        case .null: NSNull()
        case .bool(let b): b
        case .number(let n): n.rounded() == n && abs(n) < 1e15 ? NSNumber(value: Int64(n)) : NSNumber(value: n)
        case .string(let s): s
        case .array(let a): a.map(\.any)
        case .object(let o): o.mapValues(\.any)
        }
    }

    /// A field of an object; null for anything else, or a field it lacks.
    public subscript(_ key: String) -> JSON {
        if case .object(let o) = self, let v = o[key] { return v }
        return .null
    }

    /// An item of an array; null past its end, or for anything else.
    public subscript(_ index: Int) -> JSON {
        if case .array(let a) = self, a.indices.contains(index) { return a[index] }
        return .null
    }

    public var string: String? { if case .string(let s) = self { return s } else { return nil } }
    public var number: Double? { if case .number(let n) = self { return n } else { return nil } }
    public var int: Int? { number.map { Int($0) } }
    public var bool: Bool? { if case .bool(let b) = self { return b } else { return nil } }
    public var array: [JSON]? { if case .array(let a) = self { return a } else { return nil } }
    public var object: [String: JSON]? { if case .object(let o) = self { return o } else { return nil } }
    public var isNull: Bool { self == .null }

    /// A string with something besides spaces in it, else nil.
    var text: String? { string.flatMap { $0.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty ? nil : $0 } }
}

extension JSON: ExpressibleByDictionaryLiteral, ExpressibleByArrayLiteral, ExpressibleByStringLiteral,
    ExpressibleByBooleanLiteral, ExpressibleByIntegerLiteral, ExpressibleByNilLiteral {
    public init(dictionaryLiteral elements: (String, JSON)...) { self = .object(Dictionary(elements, uniquingKeysWith: { $1 })) }
    public init(arrayLiteral elements: JSON...) { self = .array(elements) }
    public init(stringLiteral value: String) { self = .string(value) }
    public init(booleanLiteral value: Bool) { self = .bool(value) }
    public init(integerLiteral value: Int) { self = .number(Double(value)) }
    public init(nilLiteral: ()) { self = .null }
}
