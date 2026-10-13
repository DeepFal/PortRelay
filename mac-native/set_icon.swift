import AppKit

// 用法: swift mac-native/set_icon.swift [PortRelay.app]（默认相对当前目录）
let appPath = CommandLine.arguments.count > 1 ? CommandLine.arguments[1] : "PortRelay.app"
let iconPath = appPath + "/Contents/Resources/AppIcon.icns"

if let img = NSImage(contentsOfFile: iconPath) {
    let ok = NSWorkspace.shared.setIcon(img, forFile: appPath, options: [])
    print("NSWorkspace setIcon result:", ok)
}
