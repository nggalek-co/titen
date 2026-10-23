import Foundation
import AVFoundation
import ScreenCaptureKit
import CoreMedia
import CoreGraphics
import Darwin

func report(_ message: String) {
    FileHandle.standardError.write(Data((message + "\n").utf8))
}

final class Capture: NSObject, SCStreamOutput, SCStreamDelegate, AVCaptureAudioDataOutputSampleBufferDelegate {
    private var stream: SCStream?
    private var microphoneSession: AVCaptureSession?
    private var microphoneOutput: AVCaptureAudioDataOutput?
    private let queue = DispatchQueue(label: "titen.audio.samples")
    private let writer = DispatchQueue(label: "titen.audio.ipc")
    private let slots = DispatchSemaphore(value: 32)
    private var overflowReported = false
    private var converters: [Int: AVAudioConverter] = [:]
    private var formats: [Int: AVAudioFormat] = [:]
    private let output = AVAudioFormat(commonFormat: .pcmFormatInt16, sampleRate: 16000, channels: 1, interleaved: true)!

    func start(deviceID: String?) async throws {
        let microphone = !CommandLine.arguments.contains("--system-only")
        let system = !CommandLine.arguments.contains("--microphone-only")
        var permitted = true
        if microphone { permitted = await AVCaptureDevice.requestAccess(for: .audio) }
        guard permitted else {
            throw NSError(domain: "TitenAudio", code: 1, userInfo: [NSLocalizedDescriptionKey: "Microphone access denied. Enable Titen in System Settings > Privacy & Security > Microphone."])
        }
        if !system {
            try startMicrophone(deviceID: deviceID)
            return
        }
        if microphone, let deviceID,
           AVCaptureDevice(uniqueID: deviceID) == nil {
            throw NSError(domain: "TitenAudio", code: 3, userInfo: [NSLocalizedDescriptionKey: "The selected microphone is unavailable. Reconnect it or choose another microphone in Settings."])
        }
        let content = try await SCShareableContent.excludingDesktopWindows(false, onScreenWindowsOnly: true)
        guard let display = content.displays.first else {
            throw NSError(domain: "TitenAudio", code: 2, userInfo: [NSLocalizedDescriptionKey: "No display available for system audio capture."])
        }
        let configuration = SCStreamConfiguration()
        configuration.width = 2
        configuration.height = 2
        configuration.minimumFrameInterval = CMTime(value: 1, timescale: 1)
        configuration.queueDepth = 3
        configuration.capturesAudio = system
        configuration.excludesCurrentProcessAudio = true
        configuration.sampleRate = 48000
        configuration.channelCount = 2
        configuration.captureMicrophone = microphone
        configuration.microphoneCaptureDeviceID = deviceID
        let filter = SCContentFilter(display: display, excludingApplications: [], exceptingWindows: [])
        let stream = SCStream(filter: filter, configuration: configuration, delegate: self)
        if system { try stream.addStreamOutput(self, type: .audio, sampleHandlerQueue: queue) }
        if microphone { try stream.addStreamOutput(self, type: .microphone, sampleHandlerQueue: queue) }
        self.stream = stream
        try await stream.startCapture()
    }

    private func startMicrophone(deviceID: String?) throws {
        let device = deviceID.flatMap { AVCaptureDevice(uniqueID: $0) } ?? (deviceID == nil ? AVCaptureDevice.default(for: .audio) : nil)
        guard let device, device.hasMediaType(.audio) else {
            throw NSError(domain: "TitenAudio", code: 3, userInfo: [NSLocalizedDescriptionKey: "The selected microphone is unavailable. Reconnect it or choose another microphone in Settings."])
        }
        let session = AVCaptureSession()
        let input = try AVCaptureDeviceInput(device: device)
        let audioOutput = AVCaptureAudioDataOutput()
        guard session.canAddInput(input), session.canAddOutput(audioOutput) else {
            throw NSError(domain: "TitenAudio", code: 4, userInfo: [NSLocalizedDescriptionKey: "Cannot capture the selected microphone. Choose another microphone in Settings."])
        }
        session.addInput(input)
        session.addOutput(audioOutput)
        audioOutput.setSampleBufferDelegate(self, queue: queue)
        session.startRunning()
        guard session.isRunning else {
            audioOutput.setSampleBufferDelegate(nil, queue: nil)
            throw NSError(domain: "TitenAudio", code: 5, userInfo: [NSLocalizedDescriptionKey: "The microphone could not start. Check Microphone access in System Settings > Privacy & Security, then try again."])
        }
        microphoneOutput = audioOutput
        microphoneSession = session
    }

    func stop() async {
        microphoneSession?.stopRunning()
        microphoneOutput?.setSampleBufferDelegate(nil, queue: nil)
        do { try await stream?.stopCapture() }
        catch { report("Could not stop capture cleanly. \(error.localizedDescription)") }
        let writer = self.writer
        await withCheckedContinuation { (continuation: CheckedContinuation<Void, Never>) in
            queue.async {
                writer.async { continuation.resume() }
            }
        }
    }

    func stream(_ stream: SCStream, didStopWithError error: Error) {
        report("Capture interrupted. Check screen recording and microphone permissions, then restart recording. \(error.localizedDescription)")
        exit(2)
    }

    func stream(_ stream: SCStream, didOutputSampleBuffer sample: CMSampleBuffer, of type: SCStreamOutputType) {
        guard type == .audio || type == .microphone else { return }
        deliver(sample, source: type == .microphone ? 1 : 2)
    }

    func captureOutput(_ output: AVCaptureOutput, didOutput sampleBuffer: CMSampleBuffer, from connection: AVCaptureConnection) {
        deliver(sampleBuffer, source: 1)
    }

    private func deliver(_ sample: CMSampleBuffer, source: Int) {
        guard sample.isValid, let description = sample.formatDescription else { return }
        let format = AVAudioFormat(cmAudioFormatDescription: description)
        let count = CMSampleBufferGetNumSamples(sample)
        guard count > 0, let input = AVAudioPCMBuffer(pcmFormat: format, frameCapacity: AVAudioFrameCount(count)) else { return }
        input.frameLength = AVAudioFrameCount(count)
        let status = CMSampleBufferCopyPCMDataIntoAudioBufferList(sample, at: 0, frameCount: Int32(count), into: input.mutableAudioBufferList)
        guard status == noErr else { report("Could not read captured PCM samples."); return }
        if formats[source] != format {
            converters[source] = AVAudioConverter(from: format, to: output)
            formats[source] = format
        }
        guard let converter = converters[source],
              let normalized = AVAudioPCMBuffer(pcmFormat: output, frameCapacity: AVAudioFrameCount(Double(count) * 16000 / format.sampleRate + 64)) else { return }
        var supplied = false
        var error: NSError?
        let result = converter.convert(to: normalized, error: &error) { _, status in
            if supplied { status.pointee = .noDataNow; return nil }
            supplied = true
            status.pointee = .haveData
            return input
        }
        guard result != .error, normalized.frameLength > 0,
              let samples = normalized.int16ChannelData?.pointee else { return }
        let seconds = sample.presentationTimeStamp.seconds
        guard seconds.isFinite, seconds >= 0 else { return }
        var packet = Data("TIT1".utf8)
        packet.append(UInt8(source))
        packet.append(1)
        func append<T: FixedWidthInteger>(_ value: T) {
            var little = value.littleEndian
            withUnsafeBytes(of: &little) { packet.append(contentsOf: $0) }
        }
        append(UInt16(1))
        append(UInt32(16000))
        append(UInt64(seconds * 1_000_000_000))
        append(UInt32(normalized.frameLength * 2))
        packet.append(UnsafeBufferPointer(start: UnsafeRawPointer(samples).assumingMemoryBound(to: UInt8.self), count: Int(normalized.frameLength) * 2))
        guard slots.wait(timeout: .now()) == .success else {
            if !overflowReported {
                report("Native audio writer queue overflow; captured audio was dropped.")
                overflowReported = true
            }
            return
        }
        writer.async { [self, packet] in
            defer { slots.signal() }
            do { try FileHandle.standardOutput.write(contentsOf: packet) }
            catch { exit(0) }
        }
    }
}

final class StopGate: @unchecked Sendable {
    private let lock = NSLock()
    private var stopping = false

    func begin() -> Bool {
        lock.lock()
        defer { lock.unlock() }
        guard !stopping else { return false }
        stopping = true
        return true
    }
}

struct PermissionStatus: Encodable {
    let microphone: Bool
    let microphoneStatus: String
    let systemAudio: Bool
    let systemAudioStatus: String
    let systemAudioError: String?
}

@main struct AudioMain {
    static func main() async {
        signal(SIGPIPE, SIG_IGN)
        if CommandLine.arguments.contains("--check-permissions") || CommandLine.arguments.contains("--request-permissions") {
            let request = CommandLine.arguments.contains("--request-permissions")
            if request {
                if !CommandLine.arguments.contains("--system-only") {
                    _ = await AVCaptureDevice.requestAccess(for: .audio)
                }
                if !CommandLine.arguments.contains("--microphone-only") {
                    _ = CGRequestScreenCaptureAccess()
                }
            }
            let status = AVCaptureDevice.authorizationStatus(for: .audio)
            let microphone = status == .authorized
            // Check the same API used by Capture.start. A Core Graphics
            // preflight failure alone is not proof that ScreenCaptureKit is denied.
            var systemAudio = CGPreflightScreenCaptureAccess()
            var systemAudioError: String?
            if !CommandLine.arguments.contains("--microphone-only") {
                do {
                    _ = try await SCShareableContent.excludingDesktopWindows(false, onScreenWindowsOnly: true)
                    systemAudio = true
                } catch {
                    systemAudio = false
                    let failure = error as NSError
                    systemAudioError = "\(failure.domain) (\(failure.code)): \(failure.localizedDescription)"
                }
            }
            let microphoneStatus: String
            switch status {
            case .authorized: microphoneStatus = "authorized"
            case .denied: microphoneStatus = "denied"
            case .restricted: microphoneStatus = "restricted"
            case .notDetermined: microphoneStatus = "notDetermined"
            @unknown default: microphoneStatus = "unknown"
            }
            let result = PermissionStatus(
                microphone: microphone,
                microphoneStatus: microphoneStatus,
                systemAudio: systemAudio,
                systemAudioStatus: systemAudio ? "authorized" : "notAuthorized",
                systemAudioError: systemAudioError
            )
            if let data = try? JSONEncoder().encode(result) {
                FileHandle.standardOutput.write(data)
            }
            return
        }
        if CommandLine.arguments.contains("--list-devices") {
            let discovery = AVCaptureDevice.DiscoverySession(deviceTypes: [.microphone, .external], mediaType: .audio, position: .unspecified)
            let devices = discovery.devices.map { ["id": $0.uniqueID, "name": $0.localizedName] }
            if let data = try? JSONSerialization.data(withJSONObject: devices) { FileHandle.standardOutput.write(data) }
            return
        }
        let args = CommandLine.arguments
        let index = args.firstIndex(of: "--microphone")
        let deviceID = index.flatMap { $0 + 1 < args.count ? args[$0 + 1] : nil }
        let capture = Capture()
        do {
            try await capture.start(deviceID: deviceID)
            signal(SIGTERM, SIG_IGN)
            signal(SIGINT, SIG_IGN)
            let terminate = DispatchSource.makeSignalSource(signal: SIGTERM, queue: .main)
            let interrupt = DispatchSource.makeSignalSource(signal: SIGINT, queue: .main)
            let gate = StopGate()
            let stop: @Sendable () -> Void = {
                guard gate.begin() else { return }
                Task { await capture.stop(); exit(0) }
            }
            DispatchQueue.global().async {
                _ = readLine()
                stop()
            }
            terminate.setEventHandler(handler: stop)
            interrupt.setEventHandler(handler: stop)
            terminate.resume()
            interrupt.resume()
            while true {
                _ = terminate
                _ = interrupt
                try await Task.sleep(nanoseconds: 1_000_000_000)
            }
        } catch {
            let nativeError = error as NSError
            report("Audio capture error: domain=\(nativeError.domain) code=\(nativeError.code) description=\(nativeError.localizedDescription)")
            if args.contains("--microphone-only") {
                report("Microphone test failed. \(error.localizedDescription)")
            } else {
                report("Audio capture failed. Enable Screen & System Audio Recording and Microphone access in System Settings > Privacy & Security. \(error.localizedDescription)")
            }
            exit(1)
        }
    }
}
