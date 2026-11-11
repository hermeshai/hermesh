import HermeshProto
import UIKit

/// The phone's keyboard for the machine (docs/computer-control.md §6.2): a
/// text field nobody sees. What the owner composes here, Chinese included,
/// is sent as text once it is committed; the row above the keyboard has the
/// keys a phone lacks, and modifiers that hold until the next key.
final class ScreenKeyField: UITextField, UITextFieldDelegate {
    var send: ((Hermesh_Screen_V1_Input) -> Void)?
    /// Called when the keyboard goes away by itself (its own dismiss key).
    var onDismiss: (() -> Void)?
    private let bar = ScreenKeyBar()

    /// The row names the keys as a Linux desktop does.
    func useLinuxModifiers() { bar.useLinuxModifiers() }

    override init(frame: CGRect) {
        // A point nobody sees: not hidden, which would take it out of the keyboard's and VoiceOver's reach.
        super.init(frame: CGRect(x: 0, y: 0, width: 1, height: 1))
        textColor = .clear
        tintColor = .clear
        borderStyle = .none
        autocorrectionType = .no
        autocapitalizationType = .none
        spellCheckingType = .no
        smartQuotesType = .no
        smartDashesType = .no
        smartInsertDeleteType = .no
        returnKeyType = .default
        delegate = self
        accessibilityIdentifier = "screen-keys"
        accessibilityLabel = String(localized: "Keyboard")
        inputAccessoryView = bar
        bar.onKey = { [weak self] key in self?.press(key) }
        bar.onHide = { [weak self] in
            self?.resignFirstResponder()
            self?.onDismiss?()
        }
        addTarget(self, action: #selector(changed), for: .editingChanged)
    }

    required init?(coder: NSCoder) { fatalError() }

    /// Committed text goes out and the field is empty again; text still being
    /// composed (marked) stays until it is chosen.
    @objc private func changed() {
        guard markedTextRange == nil, let t = text, !t.isEmpty else { return }
        text = ""
        let mods = bar.takeModifiers()
        var input = Hermesh_Screen_V1_Input()
        if mods.any, let ch = t.first {
            // A shortcut: the key that types this character, with the modifiers.
            var k = Hermesh_Screen_V1_Key()
            k.character = String(ch)
            mods.apply(&k)
            input.key = k
        } else {
            var x = Hermesh_Screen_V1_Text()
            x.text = t
            input.text = x
        }
        send?(input)
    }

    override func deleteBackward() {
        if (text ?? "").isEmpty { press(.backspace) } else { super.deleteBackward() }
    }

    func textFieldShouldReturn(_ textField: UITextField) -> Bool {
        if markedTextRange == nil { press(.return) }
        return markedTextRange != nil
    }

    private func press(_ key: Hermesh_Screen_V1_NamedKey) {
        var k = Hermesh_Screen_V1_Key()
        k.key = key
        bar.takeModifiers().apply(&k)
        var input = Hermesh_Screen_V1_Input()
        input.key = k
        send?(input)
    }
}

struct ScreenModifiers {
    var command = false, option = false, control = false, shift = false
    var any: Bool { command || option || control || shift }

    func apply(_ k: inout Hermesh_Screen_V1_Key) {
        k.command = command
        k.option = option
        k.control = control
        k.shift = shift
    }
}

/// The row above the keyboard: esc, tab, the modifiers, the arrows, and a
/// key that puts the keyboard away.
final class ScreenKeyBar: UIInputView {
    var onKey: ((Hermesh_Screen_V1_NamedKey) -> Void)?
    var onHide: (() -> Void)?
    /// ⌘, ⌥, ⌃ and ⇧, in the order takeModifiers reads them.
    private var modifiers: [UIButton] = []
    private let stack = UIStackView()

    init() {
        super.init(frame: CGRect(x: 0, y: 0, width: 320, height: 48), inputViewStyle: .keyboard)
        allowsSelfSizing = true
        stack.axis = .horizontal
        stack.spacing = 6
        stack.alignment = .center
        func key(_ title: String, _ id: String, _ action: UIAction) -> UIButton {
            var c = UIButton.Configuration.gray()
            c.title = title
            c.cornerStyle = .medium
            c.contentInsets = NSDirectionalEdgeInsets(top: 6, leading: 11, bottom: 6, trailing: 11)
            let b = UIButton(configuration: c, primaryAction: action)
            b.accessibilityIdentifier = id
            b.widthAnchor.constraint(greaterThanOrEqualToConstant: 40).isActive = true
            return b
        }
        for (title, id, k) in [("esc", "screen-key-esc", Hermesh_Screen_V1_NamedKey.escape), ("tab", "screen-key-tab", .tab)] {
            stack.addArrangedSubview(key(title, id, UIAction { [weak self] _ in self?.onKey?(k) }))
        }
        for (title, id) in [("⌘", "screen-key-command"), ("⌥", "screen-key-option"), ("⌃", "screen-key-control"), ("⇧", "screen-key-shift")] {
            let b = key(title, id, UIAction { _ in })
            b.changesSelectionAsPrimaryAction = true
            modifiers.append(b)
            stack.addArrangedSubview(b)
        }
        for (title, id, k) in [("←", "screen-key-left", Hermesh_Screen_V1_NamedKey.left), ("↓", "screen-key-down", .down), ("↑", "screen-key-up", .up), ("→", "screen-key-right", .right)] {
            stack.addArrangedSubview(key(title, id, UIAction { [weak self] _ in self?.onKey?(k) }))
        }
        var c = UIButton.Configuration.plain()
        c.image = UIImage(systemName: "keyboard.chevron.compact.down")
        let hide = UIButton(configuration: c, primaryAction: UIAction { [weak self] _ in self?.onHide?() })
        hide.accessibilityLabel = String(localized: "Hide Keyboard")
        hide.accessibilityIdentifier = "screen-key-hide"
        stack.addArrangedSubview(hide)

        let scroll = UIScrollView()
        scroll.showsHorizontalScrollIndicator = false
        scroll.translatesAutoresizingMaskIntoConstraints = false
        stack.translatesAutoresizingMaskIntoConstraints = false
        addSubview(scroll)
        scroll.addSubview(stack)
        NSLayoutConstraint.activate([
            scroll.leadingAnchor.constraint(equalTo: safeAreaLayoutGuide.leadingAnchor),
            scroll.trailingAnchor.constraint(equalTo: safeAreaLayoutGuide.trailingAnchor),
            scroll.topAnchor.constraint(equalTo: topAnchor),
            scroll.bottomAnchor.constraint(equalTo: bottomAnchor),
            scroll.heightAnchor.constraint(equalToConstant: 48),
            stack.leadingAnchor.constraint(equalTo: scroll.contentLayoutGuide.leadingAnchor, constant: 8),
            stack.trailingAnchor.constraint(equalTo: scroll.contentLayoutGuide.trailingAnchor, constant: -8),
            stack.topAnchor.constraint(equalTo: scroll.contentLayoutGuide.topAnchor),
            stack.bottomAnchor.constraint(equalTo: scroll.contentLayoutGuide.bottomAnchor),
            stack.heightAnchor.constraint(equalTo: scroll.frameLayoutGuide.heightAnchor),
        ])
    }

    required init?(coder: NSCoder) { fatalError() }

    override var intrinsicContentSize: CGSize { CGSize(width: UIView.noIntrinsicMetric, height: 48) }

    /// useLinuxModifiers names the modifiers Ctrl, Alt, Super and ⇧, in that
    /// order: they are sent as control, option and command.
    func useLinuxModifiers() {
        for (b, title) in zip(modifiers, ["Super", "Alt", "Ctrl", "⇧"]) { b.configuration?.title = title }
        for (i, b) in [modifiers[2], modifiers[1], modifiers[0], modifiers[3]].enumerated() {
            stack.removeArrangedSubview(b)
            stack.insertArrangedSubview(b, at: 2 + i)
        }
    }

    /// The modifiers held, which let go with the key they are taken for.
    func takeModifiers() -> ScreenModifiers {
        let m = ScreenModifiers(command: modifiers[0].isSelected, option: modifiers[1].isSelected, control: modifiers[2].isSelected, shift: modifiers[3].isSelected)
        modifiers.forEach { $0.isSelected = false }
        return m
    }
}
