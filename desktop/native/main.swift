import AVFoundation
import Cocoa
import CoreVideo
import Darwin
import MetalFX
import MetalKit

func fail(_ message: String) -> Never {
  fputs("SERIAL MetalFX: \(message)\n", stderr)
  exit(1)
}
func json(_ value: Any) -> String {
  String(
    data: try! JSONSerialization.data(withJSONObject: value, options: [.sortedKeys]),
    encoding: .utf8)!
}
let device = MTLCreateSystemDefaultDevice()
let supported = device.map { MTLFXSpatialScalerDescriptor.supportsDevice($0) } ?? false
if CommandLine.arguments.contains("--capabilities") {
  print(
    json([
      "available": supported, "device": device?.name ?? "",
      "reason": supported ? "" : "MetalFX spatial scaling unavailable",
    ]))
  exit(0)
}
if !supported { fail("MetalFX spatial scaling unavailable") }
let gpu = device!
let queue = gpu.makeCommandQueue()!
func scaler(_ w: Int, _ h: Int) -> MTLFXSpatialScaler {
  let d = MTLFXSpatialScalerDescriptor()
  d.inputWidth = w
  d.inputHeight = h
  d.outputWidth = w * 2
  d.outputHeight = h * 2
  d.colorTextureFormat = .bgra8Unorm
  d.outputTextureFormat = .bgra8Unorm
  d.colorProcessingMode = .perceptual
  guard let s = d.makeSpatialScaler(device: gpu) else { fail("Cannot create spatial scaler") }
  return s
}
func texture(_ w: Int, _ h: Int, _ usage: MTLTextureUsage, _ storage: MTLStorageMode) -> MTLTexture
{
  let d = MTLTextureDescriptor.texture2DDescriptor(
    pixelFormat: .bgra8Unorm, width: w, height: h, mipmapped: false)
  d.usage = usage
  d.storageMode = storage
  guard let t = gpu.makeTexture(descriptor: d) else { fail("Cannot allocate video texture") }
  return t
}
if CommandLine.arguments.contains("--self-test") {
  let s = scaler(64, 36)
  let input = texture(64, 36, s.colorTextureUsage, .shared)
  var pixels = [UInt8](repeating: 0, count: 64 * 36 * 4)
  for i in 0..<64 * 36 {
    pixels[i * 4] = UInt8(i % 256)
    pixels[i * 4 + 1] = UInt8((i / 64) * 7)
    pixels[i * 4 + 2] = 128
    pixels[i * 4 + 3] = 255
  }
  pixels.withUnsafeBytes {
    input.replace(
      region: MTLRegionMake2D(0, 0, 64, 36), mipmapLevel: 0, withBytes: $0.baseAddress!,
      bytesPerRow: 256)
  }
  let output = texture(128, 72, s.outputTextureUsage, .private)
  s.colorTexture = input
  s.outputTexture = output
  s.inputContentWidth = 64
  s.inputContentHeight = 36
  let cb = queue.makeCommandBuffer()!
  s.encode(commandBuffer: cb)
  let read = gpu.makeBuffer(length: 128 * 72 * 4, options: .storageModeShared)!
  let blit = cb.makeBlitCommandEncoder()!
  blit.copy(
    from: output, sourceSlice: 0, sourceLevel: 0, sourceOrigin: MTLOrigin(x: 0, y: 0, z: 0),
    sourceSize: MTLSize(width: 128, height: 72, depth: 1), to: read, destinationOffset: 0,
    destinationBytesPerRow: 512, destinationBytesPerImage: 128 * 72 * 4)
  blit.endEncoding()
  cb.commit()
  cb.waitUntilCompleted()
  guard cb.status == .completed else { fail("MetalFX GPU self-test failed") }
  let bytes = read.contents().assumingMemoryBound(to: UInt8.self)
  var sum: UInt64 = 0
  for i in 0..<128 * 72 * 4 { sum &+= UInt64(bytes[i]) }
  guard sum > 0 else { fail("Empty MetalFX output") }
  print(
    json([
      "available": true, "device": gpu.name, "gpuCompleted": true, "inputWidth": 64,
      "inputHeight": 36, "outputWidth": 128, "outputHeight": 72, "checksum": sum,
    ]))
  exit(0)
}
struct Subtitle: Decodable {
  let url: String
  let language: String?
  let label: String?
}
struct Config: Decodable {
  let socket: String
  let title: String
  let url: String
  let headers: [String: String]
  let subtitles: [Subtitle]
  let start: Double
  let mode: String
  let scale: Int
}
let arguments = CommandLine.arguments
if !arguments.contains("--config") { fail("Invalid configuration") }
let configIndex = arguments.firstIndex(of: "--config")! + 1
if configIndex >= arguments.count { fail("Invalid configuration") }
let configData = try Data(contentsOf: URL(fileURLWithPath: arguments[configIndex]))
let config = try JSONDecoder().decode(Config.self, from: configData)
if config.scale != 2 { fail("Invalid scale") }

struct Cue {
  let start: Double
  let end: Double
  let text: String
}
func timestamp(_ s: String) -> Double {
  let p = s.replacingOccurrences(of: ",", with: ".").split(separator: ":").compactMap { Double($0) }
  return p.reversed().enumerated().reduce(0) { $0 + $1.element * pow(60, Double($1.offset)) }
}
// Accumulate a small text track without permitting an unbounded network response.
final class SubtitleLoader: NSObject, URLSessionDataDelegate {
  static let maximumBytes = 4 * 1024 * 1024
  var bytes = Data()
  var session: URLSession?
  let completion: (Data?) -> Void
  init(request: URLRequest, completion: @escaping (Data?) -> Void) {
    self.completion = completion
    super.init()
    let configuration = URLSessionConfiguration.ephemeral
    configuration.timeoutIntervalForRequest = 20
    configuration.timeoutIntervalForResource = 30
    session = URLSession(configuration: configuration, delegate: self, delegateQueue: nil)
    session!.dataTask(with: request).resume()
  }
  func urlSession(
    _ session: URLSession, dataTask: URLSessionDataTask,
    didReceive response: URLResponse,
    completionHandler: @escaping (URLSession.ResponseDisposition) -> Void
  ) {
    let validStatus =
      (response as? HTTPURLResponse).map { (200...299).contains($0.statusCode) } ?? true
    completionHandler(
      validStatus && response.expectedContentLength <= Int64(Self.maximumBytes) ? .allow : .cancel)
  }
  func urlSession(_ session: URLSession, dataTask: URLSessionDataTask, didReceive data: Data) {
    guard data.count <= Self.maximumBytes - bytes.count else {
      dataTask.cancel()
      return
    }
    bytes.append(data)
  }
  func urlSession(_ session: URLSession, task: URLSessionTask, didCompleteWithError error: Error?) {
    completion(error == nil ? bytes : nil)
    session.finishTasksAndInvalidate()
    self.session = nil
  }
}
class Player: NSObject, NSApplicationDelegate, NSWindowDelegate, MTKViewDelegate,
  AVPlayerItemLegibleOutputPushDelegate
{
  var requestedSpeed: Double = 1
  var rememberedVolume: Double = 100
  var controls: NSView!
  var playButton: NSButton!
  var muteButton: NSButton!
  var seekSlider: NSSlider!
  var volumeSlider: NSSlider!
  var timeLabel: NSTextField!
  var speedMenu: NSPopUpButton!
  var controlsHeight: CGFloat = 88
  var subtitleLoader: SubtitleLoader?
  var legible: AVPlayerItemLegibleOutput?
  var embeddedText = ""
  var startup = Date()
  var inputTexture: MTLTexture?
  var window: NSWindow!
  var view: MTKView!
  var label: NSTextField!
  var player: AVPlayer!
  var output: AVPlayerItemVideoOutput!
  var cache: CVMetalTextureCache!
  var fx: MTLFXSpatialScaler?
  var scaled: MTLTexture?
  var frames = 0
  var width = 0
  var height = 0
  var pipeline: MTLRenderPipelineState!
  var cues = [Cue]()
  var timer: Timer?
  var observation: NSKeyValueObservation?
  var server: Int32 = -1
  func applicationDidFinishLaunching(_ note: Notification) {
    NSApp.setActivationPolicy(.regular)
    window = NSWindow(
      contentRect: NSRect(x: 0, y: 0, width: 1100, height: 650),
      styleMask: [.titled, .closable, .miniaturizable, .resizable], backing: .buffered, defer: false
    )
    window.title = config.title
    window.delegate = self
    window.contentMinSize = NSSize(width: 900, height: 400)
    window.collectionBehavior.insert(.fullScreenPrimary)
    view = MTKView(frame: window.contentView!.bounds, device: gpu)
    view.autoresizingMask = []
    view.colorPixelFormat = .bgra8Unorm
    view.framebufferOnly = true
    view.delegate = self
    view.preferredFramesPerSecond = 60
    window.contentView!.addSubview(view)
    label = NSTextField(labelWithString: "")
    label.frame = NSRect(x: 40, y: 25, width: 1020, height: 100)
    label.autoresizingMask = []
    label.alignment = .center
    label.font = .boldSystemFont(ofSize: 24)
    label.textColor = .white
    label.backgroundColor = NSColor.black.withAlphaComponent(0.7)
    label.drawsBackground = true
    label.maximumNumberOfLines = 3
    label.isHidden = true
    window.contentView!.addSubview(label)
    installControls()
    layoutPlayer()
    let click = NSClickGestureRecognizer(target: self, action: #selector(videoClicked))
    view.addGestureRecognizer(click)
    let source = """
      #include <metal_stdlib>
      using namespace metal;
      struct V { float4 p [[position]]; float2 uv; };
      vertex V vert(uint i [[vertex_id]]) { float2 p[4]={float2(-1,-1),float2(1,-1),float2(-1,1),float2(1,1)}; V v; v.p=float4(p[i],0,1); v.uv=float2((p[i].x+1)/2,(1-p[i].y)/2); return v; }
      fragment float4 frag(V v [[stage_in]],texture2d<float> t [[texture(0)]]) { constexpr sampler s(filter::linear); return t.sample(s,v.uv); }
      """
    do {
      let lib = try gpu.makeLibrary(source: source, options: nil)
      let d = MTLRenderPipelineDescriptor()
      d.vertexFunction = lib.makeFunction(name: "vert")
      d.fragmentFunction = lib.makeFunction(name: "frag")
      d.colorAttachments[0].pixelFormat = .bgra8Unorm
      pipeline = try gpu.makeRenderPipelineState(descriptor: d)
    } catch { fail("Cannot initialize display pipeline") }
    CVMetalTextureCacheCreate(nil, nil, gpu, nil, &cache)
    guard
      let url = config.url.hasPrefix("/")
        ? URL(fileURLWithPath: config.url) : URL(string: config.url)
    else { fail("Invalid video URL") }
    let asset = AVURLAsset(url: url, options: ["AVURLAssetHTTPHeaderFieldsKey": config.headers])
    let item = AVPlayerItem(asset: asset)
    output = AVPlayerItemVideoOutput(pixelBufferAttributes: [
      kCVPixelBufferPixelFormatTypeKey as String: kCVPixelFormatType_32BGRA,
      kCVPixelBufferMetalCompatibilityKey as String: true,
    ])
    item.add(output)
    let subtitlesOutput = AVPlayerItemLegibleOutput()
    subtitlesOutput.setDelegate(self, queue: .main)
    subtitlesOutput.suppressesPlayerRendering = true
    item.add(subtitlesOutput)
    legible = subtitlesOutput
    player = AVPlayer(playerItem: item)
    observation = item.observe(\.status, options: [.new]) { item, _ in
      if item.status == .failed { DispatchQueue.main.async { fail("Video decode failed") } }
    }
    NotificationCenter.default.addObserver(
      forName: .AVPlayerItemDidPlayToEndTime, object: item, queue: .main
    ) { _ in NSApp.terminate(nil) }
    NotificationCenter.default.addObserver(
      forName: .AVPlayerItemFailedToPlayToEndTime, object: item, queue: .main
    ) { _ in fail("Video playback failed") }
    if config.start > 0 { player.seek(to: CMTime(seconds: config.start, preferredTimescale: 600)) }
    player.play()
    window.center()
    window.makeKeyAndOrderFront(nil)
    NSApp.activate(ignoringOtherApps: true)
    NSEvent.addLocalMonitorForEvents(matching: .keyDown) { e in
      guard e.window == self.window else { return e }
      if self.window.firstResponder is NSControl || self.window.firstResponder is NSTextView {
        return e
      }
      if e.keyCode == 49 {
        _ = self.command(["cycle", "pause"])
        return nil
      }
      if e.keyCode == 123 || e.keyCode == 124 {
        self.seekRelative(e.keyCode == 123 ? -10 : 10)
        return nil
      }
      return e
    }
    loadSubtitles()
    startIPC()
    timer = Timer.scheduledTimer(withTimeInterval: 0.1, repeats: true) { _ in
      self.updateControls()
      let time = self.player.currentTime().seconds
      let text = self.cues.filter { time >= $0.start && time < $0.end }.map(\.text).joined(
        separator: "\n")
      let displayed = self.cues.isEmpty ? self.embeddedText : text
      self.label.stringValue = displayed
      self.label.isHidden = displayed.isEmpty
      if self.frames == 0 && Date().timeIntervalSince(self.startup) > 45 {
        fail("No decoded and upscaled video frame within startup deadline")
      }
    }
  }
  func makeButton(_ title: String, _ action: Selector) -> NSButton {
    let button = NSButton(title: title, target: self, action: action)
    button.bezelStyle = .rounded
    button.translatesAutoresizingMaskIntoConstraints = false
    return button
  }
  func installControls() {
    controls = NSView()
    controls.wantsLayer = true
    controls.layer?.backgroundColor = NSColor.windowBackgroundColor.cgColor
    window.contentView!.addSubview(controls)
    playButton = makeButton("Pause", #selector(togglePlayback))
    let back = makeButton("−10 s", #selector(rewind))
    let forward = makeButton("+10 s", #selector(fastForward))
    muteButton = makeButton("Mute", #selector(toggleMute))
    let fullscreen = makeButton("Full screen", #selector(toggleFullscreen))
    speedMenu = NSPopUpButton()
    speedMenu.addItems(withTitles: ["0.5×", "0.75×", "1×", "1.25×", "1.5×", "2×"])
    speedMenu.selectItem(at: 2)
    speedMenu.target = self
    speedMenu.action = #selector(changeSpeed)
    speedMenu.setAccessibilityLabel("Playback speed")
    volumeSlider = NSSlider(
      value: 100, minValue: 0, maxValue: 100, target: self, action: #selector(changeVolume))
    volumeSlider.setAccessibilityLabel("Volume")
    volumeSlider.widthAnchor.constraint(equalToConstant: 130).isActive = true
    seekSlider = NSSlider(
      value: 0, minValue: 0, maxValue: 1, target: self, action: #selector(changePosition))
    seekSlider.isContinuous = false
    seekSlider.setAccessibilityLabel("Playback position")
    timeLabel = NSTextField(labelWithString: "0:00 / 0:00")
    timeLabel.font = .monospacedDigitSystemFont(ofSize: 12, weight: .regular)
    timeLabel.widthAnchor.constraint(equalToConstant: 145).isActive = true
    let timeline = NSStackView(views: [seekSlider, timeLabel])
    timeline.orientation = .horizontal
    timeline.spacing = 12
    let speedLabel = NSTextField(labelWithString: "Speed")
    let buttons = NSStackView(views: [
      playButton, back, forward, speedLabel, speedMenu, muteButton, volumeSlider, fullscreen,
    ])
    buttons.orientation = .horizontal
    buttons.spacing = 10
    let stack = NSStackView(views: [timeline, buttons])
    stack.orientation = .vertical
    stack.alignment = .leading
    stack.spacing = 8
    stack.translatesAutoresizingMaskIntoConstraints = false
    controls.addSubview(stack)
    NSLayoutConstraint.activate([
      stack.leadingAnchor.constraint(equalTo: controls.leadingAnchor, constant: 16),
      stack.trailingAnchor.constraint(equalTo: controls.trailingAnchor, constant: -16),
      stack.centerYAnchor.constraint(equalTo: controls.centerYAnchor),
      timeline.widthAnchor.constraint(equalTo: stack.widthAnchor),
    ])
  }
  func layoutPlayer() {
    guard let content = window.contentView, controls != nil else { return }
    let bounds = content.bounds
    controls.frame = NSRect(x: 0, y: 0, width: bounds.width, height: controlsHeight)
    view.frame = NSRect(
      x: 0, y: controlsHeight, width: bounds.width, height: max(1, bounds.height - controlsHeight))
    label.frame = NSRect(
      x: 30, y: controlsHeight + 18, width: max(1, bounds.width - 60),
      height: min(100, max(1, bounds.height - controlsHeight - 36)))
  }
  func windowDidResize(_ notification: Notification) { layoutPlayer() }
  func windowDidEnterFullScreen(_ notification: Notification) { layoutPlayer() }
  func windowDidExitFullScreen(_ notification: Notification) { layoutPlayer() }
  func clockText(_ value: Double) -> String {
    let seconds = Int(max(0, value.isFinite ? value : 0))
    return seconds >= 3600
      ? String(format: "%d:%02d:%02d", seconds / 3600, seconds / 60 % 60, seconds % 60)
      : String(format: "%d:%02d", seconds / 60, seconds % 60)
  }
  func updateControls() {
    playButton.title = player.rate == 0 ? "Play" : "Pause"
    muteButton.title = player.volume == 0 ? "Unmute" : "Mute"
    volumeSlider.doubleValue = Double(player.volume) * 100
    let position = player.currentTime().seconds
    let duration = player.currentItem?.duration.seconds ?? 0
    let validDuration = duration.isFinite && duration > 0
    seekSlider.isEnabled = validDuration
    seekSlider.maxValue = validDuration ? duration : 1
    seekSlider.doubleValue = position.isFinite ? min(seekSlider.maxValue, max(0, position)) : 0
    timeLabel.stringValue =
      clockText(position) + " / " + (validDuration ? clockText(duration) : "—")
    let speeds: [Double] = [0.5, 0.75, 1, 1.25, 1.5, 2]
    if let index = speeds.firstIndex(of: requestedSpeed) { speedMenu.selectItem(at: index) }
  }
  func seekRelative(_ delta: Double) {
    let position = player.currentTime().seconds
    _ = command(["seek", (position.isFinite ? position : 0) + delta, "absolute"])
  }
  @objc func togglePlayback() { _ = command(["cycle", "pause"]) }
  @objc func videoClicked() {
    window.makeFirstResponder(nil)
    togglePlayback()
  }
  @objc func rewind() { seekRelative(-10) }
  @objc func fastForward() { seekRelative(10) }
  @objc func changePosition() { _ = command(["seek", seekSlider.doubleValue, "absolute"]) }
  @objc func changeVolume() { _ = command(["set_property", "volume", volumeSlider.doubleValue]) }
  @objc func toggleMute() {
    let volume: Double = player.volume == 0 ? rememberedVolume : 0.0
    _ = command(["set_property", "volume", volume])
  }
  @objc func changeSpeed() {
    let speeds: [Double] = [0.5, 0.75, 1, 1.25, 1.5, 2]
    _ = command(["set_property", "speed", speeds[speedMenu.indexOfSelectedItem]])
  }
  @objc func toggleFullscreen() { window.toggleFullScreen(nil) }
  func setPaused(_ paused: Bool) {
    if paused {
      player.pause()
    } else {
      player.defaultRate = Float(requestedSpeed)
      player.play()
    }
  }
  func legibleOutput(
    _ output: AVPlayerItemLegibleOutput, didOutputAttributedStrings strings: [NSAttributedString],
    nativeSampleBuffers: [Any], forItemTime itemTime: CMTime
  ) { embeddedText = strings.map(\.string).joined(separator: "\n") }
  func loadSubtitles() {
    guard
      let sub = config.subtitles.first(where: {
        ($0.language ?? "").lowercased().hasPrefix("en")
          || ($0.label ?? "").lowercased().contains("english")
      }), let url = URL(string: sub.url)
    else { return }
    var request = URLRequest(url: url)
    for (k, v) in config.headers { request.setValue(v, forHTTPHeaderField: k) }
    subtitleLoader = SubtitleLoader(request: request) { data in
      guard let data = data, let text = String(data: data, encoding: .utf8) else { return }
      var result = [Cue]()
      for block in text.replacingOccurrences(of: "\r", with: "").components(separatedBy: "\n\n") {
        let lines = block.components(separatedBy: "\n")
        guard let i = lines.firstIndex(where: { $0.contains(" --> ") }) else { continue }
        let times = lines[i].components(separatedBy: " --> ")
        guard times.count == 2 else { continue }
        let end = String(times[1].split(separator: " ").first ?? "0")
        let body = lines.dropFirst(i + 1).joined(separator: "\n").replacingOccurrences(
          of: "<[^>]+>", with: "", options: .regularExpression)
        result.append(Cue(start: timestamp(times[0]), end: timestamp(end), text: body))
      }
      DispatchQueue.main.async {
        self.cues = result
        self.subtitleLoader = nil
      }
    }
  }
  func mtkView(_ view: MTKView, drawableSizeWillChange size: CGSize) {}
  func draw(in view: MTKView) {
    let time = output.itemTime(forHostTime: CACurrentMediaTime())
    guard output.hasNewPixelBuffer(forItemTime: time),
      let pb = output.copyPixelBuffer(forItemTime: time, itemTimeForDisplay: nil),
      let drawable = view.currentDrawable, let pass = view.currentRenderPassDescriptor
    else {
      presentRetainedFrame(view)
      return
    }
    let w = CVPixelBufferGetWidth(pb)
    let h = CVPixelBufferGetHeight(pb)
    if w != width || h != height {
      width = w
      height = h
      fx = scaler(w, h)
      inputTexture = texture(w, h, fx!.colorTextureUsage, .private)
      scaled = texture(w * 2, h * 2, fx!.outputTextureUsage.union(.shaderRead), .private)
    }
    var cv: CVMetalTexture?
    guard
      CVMetalTextureCacheCreateTextureFromImage(nil, cache, pb, nil, .bgra8Unorm, w, h, 0, &cv)
        == kCVReturnSuccess, let cv = cv, let input = CVMetalTextureGetTexture(cv),
      let cb = queue.makeCommandBuffer()
    else { fail("Cannot map decoded frame to Metal") }
    let copy = cb.makeBlitCommandEncoder()!
    copy.copy(
      from: input, sourceSlice: 0, sourceLevel: 0, sourceOrigin: MTLOrigin(x: 0, y: 0, z: 0),
      sourceSize: MTLSize(width: w, height: h, depth: 1), to: inputTexture!, destinationSlice: 0,
      destinationLevel: 0, destinationOrigin: MTLOrigin(x: 0, y: 0, z: 0))
    copy.endEncoding()
    fx!.colorTexture = inputTexture
    fx!.outputTexture = scaled
    fx!.inputContentWidth = w
    fx!.inputContentHeight = h
    fx!.encode(commandBuffer: cb)
    pass.colorAttachments[0].clearColor = MTLClearColorMake(0, 0, 0, 1)
    let enc = cb.makeRenderCommandEncoder(descriptor: pass)!
    let dw = Double(drawable.texture.width)
    let dh = Double(drawable.texture.height)
    let ratio = min(dw / Double(w), dh / Double(h))
    enc.setViewport(
      MTLViewport(
        originX: (dw - Double(w) * ratio) / 2, originY: (dh - Double(h) * ratio) / 2,
        width: Double(w) * ratio, height: Double(h) * ratio, znear: 0, zfar: 1))
    enc.setRenderPipelineState(pipeline)
    enc.setFragmentTexture(scaled, index: 0)
    enc.drawPrimitives(type: .triangleStrip, vertexStart: 0, vertexCount: 4)
    enc.endEncoding()
    cb.present(drawable)
    cb.addCompletedHandler { command in
      _ = cv
      _ = pb
      if command.status == .error {
        DispatchQueue.main.async { fail("MetalFX GPU rendering failed") }
      } else {
        DispatchQueue.main.async { self.frames += 1 }
      }
    }
    cb.commit()
  }
  func presentRetainedFrame(_ view: MTKView) {
    guard frames > 0, let scaled = scaled, let drawable = view.currentDrawable,
      let pass = view.currentRenderPassDescriptor, let cb = queue.makeCommandBuffer()
    else { return }
    pass.colorAttachments[0].clearColor = MTLClearColorMake(0, 0, 0, 1)
    guard let enc = cb.makeRenderCommandEncoder(descriptor: pass) else {
      fail("Cannot redraw video")
    }
    let dw = Double(drawable.texture.width)
    let dh = Double(drawable.texture.height)
    let ratio = min(dw / Double(width), dh / Double(height))
    enc.setViewport(
      MTLViewport(
        originX: (dw - Double(width) * ratio) / 2,
        originY: (dh - Double(height) * ratio) / 2, width: Double(width) * ratio,
        height: Double(height) * ratio, znear: 0, zfar: 1))
    enc.setRenderPipelineState(pipeline)
    enc.setFragmentTexture(scaled, index: 0)
    enc.drawPrimitives(type: .triangleStrip, vertexStart: 0, vertexCount: 4)
    enc.endEncoding()
    cb.present(drawable)
    cb.addCompletedHandler { command in
      if command.status == .error { DispatchQueue.main.async { fail("MetalFX redraw failed") } }
    }
    cb.commit()
  }
  func windowWillClose(_ note: Notification) { NSApp.terminate(nil) }
  func applicationWillTerminate(_ note: Notification) {
    if server >= 0 { close(server) }
    unlink(config.socket)
  }
  func command(_ values: [Any]) -> (String, Any) {
    guard let name = values.first as? String else { return ("invalid parameter", NSNull()) }
    let prop = values.count > 1 ? values[1] as? String ?? "" : ""
    if name == "get_property" {
      switch prop {
      case "mpv-version":
        return frames > 0 ? ("success", "SERIAL MetalFX 1") : ("property unavailable", NSNull())
      case "time-pos":
        return ("success", player.currentTime().seconds.isFinite ? player.currentTime().seconds : 0)
      case "duration":
        let d = player.currentItem?.duration.seconds ?? 0
        return ("success", d.isFinite ? d : 0)
      case "pause": return ("success", player.rate == 0)
      case "speed": return ("success", requestedSpeed)
      case "volume": return ("success", Double(player.volume) * 100)
      case "metalfx-status":
        return (
          "success",
          [
            "upscaling": frames > 0, "videoWidth": width, "videoHeight": height,
            "outputWidth": width * 2, "outputHeight": height * 2, "scaledFrames": frames,
          ]
        )
      default: return ("property unavailable", NSNull())
      }
    }
    if name == "cycle", prop == "pause" {
      setPaused(player.rate != 0)
      return ("success", NSNull())
    }
    if name == "set_property", values.count > 2 {
      if prop == "pause", let paused = values[2] as? Bool {
        setPaused(paused)
        return ("success", NSNull())
      }
      if prop == "speed", let n = values[2] as? Double {
        guard n.isFinite, n >= 0.5, n <= 2 else { return ("invalid parameter", NSNull()) }
        let paused = player.rate == 0
        requestedSpeed = n
        player.defaultRate = Float(n)
        if !paused { player.play() }
        return ("success", NSNull())
      }
      if prop == "volume", let n = values[2] as? Double {
        guard n.isFinite, n >= 0, n <= 100 else { return ("invalid parameter", NSNull()) }
        if n == 0 && player.volume > 0 { rememberedVolume = Double(player.volume) * 100 }
        if n > 0 { rememberedVolume = n }
        player.volume = Float(n / 100)
        return ("success", NSNull())
      }
    }
    if name == "seek", values.count > 1, let n = values[1] as? Double, n.isFinite {
      let duration = player.currentItem?.duration.seconds ?? 0
      let position = duration.isFinite && duration > 0 ? min(duration, max(0, n)) : max(0, n)
      player.seek(to: CMTime(seconds: position, preferredTimescale: 600))
      return ("success", NSNull())
    }
    if name == "quit" {
      DispatchQueue.main.asyncAfter(deadline: .now() + 0.1) { NSApp.terminate(nil) }
      return ("success", NSNull())
    }
    return ("command not found", NSNull())
  }
  func startIPC() {
    let path = config.socket
    let directory = URL(fileURLWithPath: path).deletingLastPathComponent().path
    var directoryInfo = stat()
    guard lstat(directory, &directoryInfo) == 0, directoryInfo.st_uid == getuid(),
      directoryInfo.st_mode & 0o777 == 0o700, directoryInfo.st_mode & S_IFMT == S_IFDIR
    else { fail("IPC directory must be owned by current user and mode 0700") }
    guard path.utf8.count < 104 else { fail("IPC path too long") }
    server = socket(AF_UNIX, SOCK_STREAM, 0)
    guard server >= 0 else { fail("Cannot create IPC socket") }
    var address = sockaddr_un()
    address.sun_family = sa_family_t(AF_UNIX)
    _ = path.withCString { p in
      withUnsafeMutablePointer(to: &address.sun_path) { ptr in
        ptr.withMemoryRebound(to: CChar.self, capacity: 104) { strcpy($0, p) }
      }
    }
    unlink(path)
    let bound = withUnsafePointer(to: &address) {
      $0.withMemoryRebound(to: sockaddr.self, capacity: 1) {
        Darwin.bind(server, $0, socklen_t(MemoryLayout<sockaddr_un>.size))
      }
    }
    guard bound == 0, chmod(path, 0o600) == 0, listen(server, 8) == 0 else {
      fail("Cannot bind private IPC socket")
    }
    let fd = server
    DispatchQueue.global().async {
      while true {
        let client = accept(fd, nil, nil)
        if client < 0 { return }
        var deadline = timeval(tv_sec: 3, tv_usec: 0)
        setsockopt(
          client, SOL_SOCKET, SO_RCVTIMEO, &deadline, socklen_t(MemoryLayout<timeval>.size))
        setsockopt(
          client, SOL_SOCKET, SO_SNDTIMEO, &deadline, socklen_t(MemoryLayout<timeval>.size))
        var noSig: Int32 = 1
        setsockopt(client, SOL_SOCKET, SO_NOSIGPIPE, &noSig, 4)
        var bytes = Data()
        var buffer = [UInt8](repeating: 0, count: 4096)
        while bytes.count < 1_048_576 {
          let n = read(client, &buffer, buffer.count)
          if n <= 0 { break }
          bytes.append(contentsOf: buffer.prefix(n))
          while let newline = bytes.firstIndex(of: 10) {
            let line = bytes.prefix(upTo: newline)
            bytes.removeSubrange(...newline)
            if let request = try? JSONSerialization.jsonObject(with: line) as? [String: Any],
              let commands = request["command"] as? [Any]
            {
              var response = ""
              DispatchQueue.main.sync {
                let (error, result) = self.command(commands)
                response =
                  json([
                    "error": error, "data": result, "request_id": request["request_id"] ?? NSNull(),
                  ]) + "\n"
              }
              response.withCString { p in _ = write(client, p, strlen(p)) }
            }
          }
        }
        close(client)
      }
    }
  }
}
let app = NSApplication.shared
let delegate = Player()
app.delegate = delegate
app.run()
