import AppKit

let size: CGFloat = 1024
let img = NSImage(size: NSSize(width: size, height: size))
img.lockFocus()

guard let ctx = NSGraphicsContext.current?.cgContext else {
    exit(1)
}

// 1. Draw rounded squircle background (Apple style macOS icon radius)
let rect = NSRect(x: 80, y: 80, width: 864, height: 864)
let path = NSBezierPath(roundedRect: rect, xRadius: 190, yRadius: 190)

// Shadow
ctx.saveGState()
ctx.setShadow(offset: CGSize(width: 0, height: -20), blur: 50, color: NSColor.black.withAlphaComponent(0.6).cgColor)
NSColor.black.setFill()
path.fill()
ctx.restoreGState()

// Gradient Fill
let colorSpace = CGColorSpaceCreateDeviceRGB()
let gradientColors = [
    NSColor(red: 0.05, green: 0.10, blue: 0.22, alpha: 1.0).cgColor,
    NSColor(red: 0.08, green: 0.16, blue: 0.35, alpha: 1.0).cgColor,
    NSColor(red: 0.02, green: 0.45, blue: 0.75, alpha: 1.0).cgColor
] as CFArray
let locations: [CGFloat] = [0.0, 0.5, 1.0]
if let gradient = CGGradient(colorsSpace: colorSpace, colors: gradientColors, locations: locations) {
    ctx.saveGState()
    path.addClip()
    ctx.drawLinearGradient(gradient, start: CGPoint(x: 80, y: 944), end: CGPoint(x: 944, y: 80), options: [])
    
    // Border highlight
    ctx.setStrokeColor(NSColor.white.withAlphaComponent(0.2).cgColor)
    ctx.setLineWidth(4)
    ctx.stroke(rect.insetBy(dx: 2, dy: 2))
    
    ctx.restoreGState()
}

// 2. Outer glowing ring (Network relay portal)
ctx.saveGState()
ctx.setShadow(offset: .zero, blur: 40, color: NSColor(red: 0.22, green: 0.74, blue: 0.97, alpha: 0.8).cgColor)
let ringRect = NSRect(x: 232, y: 232, width: 560, height: 560)
let ringPath = NSBezierPath(ovalIn: ringRect)
ringPath.lineWidth = 28
NSColor(red: 0.22, green: 0.74, blue: 0.97, alpha: 0.9).setStroke()
ringPath.stroke()
ctx.restoreGState()

// 3. Inner lightning bolt & relay arrow
let boltPath = NSBezierPath()
// Center coordinates
let cx: CGFloat = 512
let cy: CGFloat = 512

boltPath.move(to: NSPoint(x: cx + 30, y: cy + 240))
boltPath.line(to: NSPoint(x: cx - 140, y: cy + 20))
boltPath.line(to: NSPoint(x: cx - 10, y: cy + 20))
boltPath.line(to: NSPoint(x: cx - 50, y: cy - 240))
boltPath.line(to: NSPoint(x: cx + 150, y: cy - 20))
boltPath.line(to: NSPoint(x: cx + 15, y: cy - 20))
boltPath.close()

ctx.saveGState()
ctx.setShadow(offset: .zero, blur: 50, color: NSColor.white.withAlphaComponent(0.9).cgColor)
NSColor.white.setFill()
boltPath.fill()
ctx.restoreGState()

img.unlockFocus()

// Save iconset
// 在仓库根目录运行（build-app.sh 会先 cd 到仓库根目录）
let iconsetDir = "mac-native/AppIcon.iconset"
try? FileManager.default.createDirectory(atPath: iconsetDir, withIntermediateDirectories: true)

let sizes: [(String, Int)] = [
    ("icon_16x16.png", 16),
    ("icon_16x16@2x.png", 32),
    ("icon_32x32.png", 32),
    ("icon_32x32@2x.png", 64),
    ("icon_128x128.png", 128),
    ("icon_128x128@2x.png", 256),
    ("icon_256x256.png", 256),
    ("icon_256x256@2x.png", 512),
    ("icon_512x512.png", 512),
    ("icon_512x512@2x.png", 1024)
]

for (name, sz) in sizes {
    let destImg = NSImage(size: NSSize(width: sz, height: sz))
    destImg.lockFocus()
    img.draw(in: NSRect(x: 0, y: 0, width: sz, height: sz))
    destImg.unlockFocus()
    if let tiff = destImg.tiffRepresentation,
       let rep = NSBitmapImageRep(data: tiff),
       let png = rep.representation(using: .png, properties: [:]) {
        let filePath = "\(iconsetDir)/\(name)"
        try? png.write(to: URL(fileURLWithPath: filePath))
    }
}

print("Iconset PNGs generated successfully.")
