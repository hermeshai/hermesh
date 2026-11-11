import HermeshProto
import UIKit
import UniformTypeIdentifiers

/// The phone's keyboard for the machine (docs/computer-control.md §6.2): a
/// text field nobody sees. What the owner composes here, Chinese included,
/// is sent as text once it is committed; the row above the keyboard has the
/// keys a phone lacks, modifiers that hold until the next key, and Paste,
/// which gives the machine the phone's clipboard and pastes it there.
final class ScreenKeyField: UITextField, UITextFieldDelegate {
    var send: ((Hermesh_Screen_V1_Input) -> Void)?
    /// Called when the keyboard goes away by itself (its own dismiss key).
    var onDismiss: (() -> Void)?
    /// Called when the clipboard holds more than the machine takes.
    var onPasteTooLong: (() -> Void)?
    private let bar = ScreenKeyBar()

    /// The row names the keys as a Linux desktop does, and offers to paste as an app or as a terminal does.
    func useLinux() { bar.useLinux() }

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
        bar.onPaste = { [weak self] text in self?.pasteToMachine(text) }
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
            x.paste = bar.pasteStyle // how the machine pastes what it can't type
            input.text = x
        }
        send?(input)
    }

    /// The system's Paste (a hardware keyboard's Command-V) goes to the machine's clipboard and is pasted
    /// there in one go, not typed key by key.
    override func paste(_ sender: Any?) {
        if let t = UIPasteboard.general.string { pasteToMachine(t) }
    }

    /// The most text a clipboard carries either way, in UTF-8 bytes (internal/screen ClipboardMax).
    static let clipboardMax = 60000

    private func pasteToMachine(_ text: String) {
        guard !text.isEmpty else { return }
        guard text.utf8.count <= Self.clipboardMax else { onPasteTooLong?(); return }
        var c = Hermesh_Screen_V1_Clipboard()
        c.text = text
        c.paste = bar.pasteStyle
        var input = Hermesh_Screen_V1_Input()
        input.clipboard = c
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

/// The row above the keyboard: Paste (on Linux with how to paste), esc,
/// tab, the modifiers, the arrows, and a key that puts the keyboard away.
final class ScreenKeyBar: UIInputView {
    var onKey: ((Hermesh_Screen_V1_NamedKey) -> Void)?
    var onHide: (() -> Void)?
    /// The phone's clipboard, given by its Paste key.
    var onPaste: ((String) -> Void)?
    /// ⌘, ⌥, ⌃ and ⇧, in the order takeModifiers reads them.
    private var modifiers: [UIButton] = []
    private let stack = UIStackView()
    /// The system's Paste key: it reads the clipboard without asking, the owner's tap being the consent.
    private let pasteKey: UIPasteControl = {
        let c = UIPasteControl.Configuration()
        c.displayMode = .iconAndLabel
        c.cornerStyle = .medium
        // As dark as the other keys: the system draws this key itself, in the light look unless told.
        c.baseForegroundColor = .white
        c.baseBackgroundColor = UIColor.systemGray5.resolvedColor(with: UITraitCollection(userInterfaceStyle: .dark))
        return UIPasteControl(configuration: c)
    }()
    /// On Linux: paste as a terminal does (Ctrl+Shift+V), not as an app does; the app remembers it.
    private var terminal = UserDefaults.standard.bool(forKey: "screen-paste-terminal")
    private var linux = false

    /// How the machine pastes: as an app does on a Mac, as the owner chose on Linux.
    var pasteStyle: Hermesh_Screen_V1_Paste { linux && terminal ? .terminal : .app }

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
        // First, where it is in sight without moving the row: the system's key works only fully shown.
        pasteConfiguration = UIPasteConfiguration(acceptableTypeIdentifiers: [UTType.plainText.identifier])
        pasteKey.target = self
        pasteKey.accessibilityIdentifier = "screen-key-paste"
        stack.insertArrangedSubview(pasteKey, at: 0)
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

    /// useLinux names the modifiers Ctrl, Alt, Super and ⇧, in that order:
    /// they are sent as control, option and command. Beside Paste it puts how
    /// the machine pastes: as an app does (Ctrl+V), or as a terminal does
    /// (Ctrl+Shift+V).
    func useLinux() {
        linux = true
        for (b, title) in zip(modifiers, ["Super", "Alt", "Ctrl", "⇧"]) { b.configuration?.title = title }
        let first = stack.arrangedSubviews.firstIndex(of: modifiers[0])!
        for (i, b) in [modifiers[2], modifiers[1], modifiers[0], modifiers[3]].enumerated() {
            stack.removeArrangedSubview(b)
            stack.insertArrangedSubview(b, at: first + i)
        }
        var c = UIButton.Configuration.gray()
        c.cornerStyle = .medium
        c.indicator = .popup
        c.contentInsets = NSDirectionalEdgeInsets(top: 6, leading: 9, bottom: 6, trailing: 9)
        let style = UIButton(configuration: c)
        style.showsMenuAsPrimaryAction = true
        style.accessibilityIdentifier = "screen-paste-style"
        style.accessibilityLabel = String(localized: "Paste As")
        func show() {
            style.configuration?.title = terminal ? "Ctrl+⇧+V" : "Ctrl+V"
            style.accessibilityValue = terminal ? String(localized: "Terminal (Ctrl+Shift+V)") : String(localized: "App (Ctrl+V)")
            style.menu = UIMenu(title: String(localized: "Paste As"), children: [false, true].map { t in
                UIAction(title: t ? String(localized: "Terminal (Ctrl+Shift+V)") : String(localized: "App (Ctrl+V)"), state: t == terminal ? .on : .off) { [weak self] _ in
                    self?.terminal = t
                    UserDefaults.standard.set(t, forKey: "screen-paste-terminal")
                    show()
                }
            })
        }
        show()
        stack.insertArrangedSubview(style, at: stack.arrangedSubviews.firstIndex(of: pasteKey)! + 1)
    }

    /// The Paste key's text, read once the owner tapped it.
    override func paste(itemProviders: [NSItemProvider]) {
        guard let p = itemProviders.first(where: { $0.canLoadObject(ofClass: String.self) }) else { return }
        _ = p.loadObject(ofClass: String.self) { text, _ in
            guard let text else { return }
            DispatchQueue.main.async { [weak self] in self?.onPaste?(text) }
        }
    }

    /// The modifiers held, which let go with the key they are taken for.
    func takeModifiers() -> ScreenModifiers {
        let m = ScreenModifiers(command: modifiers[0].isSelected, option: modifiers[1].isSelected, control: modifiers[2].isSelected, shift: modifiers[3].isSelected)
        modifiers.forEach { $0.isSelected = false }
        return m
    }
}
