#include "pcm.hpp"
#include <iostream>
#include <string>
#ifdef _WIN32
#include <windows.h>
#include <mmdeviceapi.h>
#include <audioclient.h>
#include <functiondiscoverykeys_devpkey.h>
#include <ksmedia.h>
#include <propvarutil.h>
#include <wrl/client.h>
#include <io.h>
#include <fcntl.h>
#include <atomic>
#include <condition_variable>
#include <deque>
#include <mutex>
#include <thread>
#include <sstream>
using Microsoft::WRL::ComPtr;
namespace {
std::atomic<bool> stopped{false}, failed{false};
std::mutex log_mutex;
void report(const std::string& message) {
    std::lock_guard<std::mutex> lock(log_mutex);
    std::cerr << message << std::endl;
}
void check(HRESULT hr, const char* operation, uint8_t source = 0) {
    if (SUCCEEDED(hr)) return;
    std::ostringstream text;
    text << operation << " failed (0x" << std::hex << static_cast<uint32_t>(hr) << "). ";
    if (hr == E_ACCESSDENIED && source == 1) text << "Enable microphone access for desktop apps in Windows Settings > Privacy & security > Microphone.";
    else if (hr == E_ACCESSDENIED) text << "Windows denied access to the audio endpoint.";
    else if (hr == AUDCLNT_E_DEVICE_INVALIDATED) text << "The audio device changed or disconnected. Select an available device and start recording again.";
    else text << "Check that an enabled audio device is connected and no exclusive-mode app owns it.";
    throw std::runtime_error(text.str());
}
struct Apartment {
    Apartment() { check(CoInitializeEx(nullptr, COINIT_MULTITHREADED), "Initialize Windows audio"); }
    ~Apartment() { CoUninitialize(); }
};
struct MixFormat { WAVEFORMATEX* value = nullptr; ~MixFormat() { CoTaskMemFree(value); } };
struct StartedClient { IAudioClient* value; ~StartedClient() { value->Stop(); } };
struct CaptureEvent {
    HANDLE value = CreateEventW(nullptr, FALSE, FALSE, nullptr);
    ~CaptureEvent() { if (value) CloseHandle(value); }
};
struct Writer {
    std::mutex mutex;
    std::condition_variable changed;
    std::deque<std::vector<uint8_t>> queue;
    bool closed = false, warned = false;
    void push(std::vector<uint8_t> packet) {
        std::lock_guard<std::mutex> lock(mutex);
        if (queue.size() >= 128) {
            queue.pop_front();
            if (!warned) { report("Audio output queue overflow. Captured audio was dropped."); warned = true; }
        }
        queue.push_back(std::move(packet)); changed.notify_one();
    }
    void run() {
        for (;;) {
            std::vector<uint8_t> packet;
            {
                std::unique_lock<std::mutex> lock(mutex);
                changed.wait(lock, [&] { return closed || !queue.empty(); });
                if (queue.empty()) return;
                packet = std::move(queue.front()); queue.pop_front();
            }
            std::cout.write(reinterpret_cast<const char*>(packet.data()), static_cast<std::streamsize>(packet.size()));
            std::cout.flush();
            if (!std::cout) { stopped = true; failed = true; return; }
        }
    }
    void close() { std::lock_guard<std::mutex> lock(mutex); closed = true; changed.notify_all(); }
};
std::string utf8(const wchar_t* value) {
    int count = WideCharToMultiByte(CP_UTF8, 0, value, -1, nullptr, 0, nullptr, nullptr);
    if (!count) throw std::runtime_error("Cannot encode Windows device name");
    std::string result(static_cast<size_t>(count), 0);
    WideCharToMultiByte(CP_UTF8, 0, value, -1, result.data(), count, nullptr, nullptr);
    result.pop_back(); return result;
}
std::string json(const std::string& value) {
    std::string result = "\"";
    for (unsigned char c : value) {
        if (c == '\\' || c == '"') { result += '\\'; result += static_cast<char>(c); }
        else if (c < 32) {
            const char* hex = "0123456789abcdef";
            result += "\\u00"; result += hex[c >> 4]; result += hex[c & 15];
        } else result += static_cast<char>(c);
    }
    return result + "\"";
}
ComPtr<IMMDeviceEnumerator> enumerator() {
    ComPtr<IMMDeviceEnumerator> value;
    check(CoCreateInstance(__uuidof(MMDeviceEnumerator), nullptr, CLSCTX_ALL, IID_PPV_ARGS(value.GetAddressOf())), "Enumerate Windows audio devices");
    return value;
}
void list_devices() {
    Apartment apartment; auto devices = enumerator();
    ComPtr<IMMDeviceCollection> collection;
    check(devices->EnumAudioEndpoints(eCapture, DEVICE_STATE_ACTIVE, collection.GetAddressOf()), "List microphones");
    UINT count = 0; check(collection->GetCount(&count), "Count microphones");
    std::cout << "[";
    for (UINT i = 0; i < count; ++i) {
        ComPtr<IMMDevice> device; check(collection->Item(i, device.GetAddressOf()), "Read microphone");
        LPWSTR id = nullptr; check(device->GetId(&id), "Read microphone identifier");
        std::string identifier = utf8(id); CoTaskMemFree(id);
        ComPtr<IPropertyStore> properties;
        check(device->OpenPropertyStore(STGM_READ, properties.GetAddressOf()), "Read microphone properties");
        PROPVARIANT name; PropVariantInit(&name);
        HRESULT hr = properties->GetValue(PKEY_Device_FriendlyName, &name);
        if (FAILED(hr)) { PropVariantClear(&name); check(hr, "Read microphone name"); }
        std::string label = name.vt == VT_LPWSTR ? utf8(name.pwszVal) : identifier;
        PropVariantClear(&name);
        if (i) std::cout << ",";
        std::cout << "{\"id\":" << json(identifier) << ",\"name\":" << json(label) << "}";
    }
    std::cout << "]\n";
}
void capture(uint8_t source, std::wstring microphone, Writer& writer) {
    try {
        Apartment apartment; auto devices = enumerator();
        ComPtr<IMMDevice> device;
        if (source == 1 && !microphone.empty())
            check(devices->GetDevice(microphone.c_str(), device.GetAddressOf()), "Open selected microphone");
        else check(devices->GetDefaultAudioEndpoint(source == 1 ? eCapture : eRender, eConsole, device.GetAddressOf()), "Open default audio device");
        LPWSTR device_id = nullptr;
        check(device->GetId(&device_id), "Read capture device identifier");
        const std::wstring active_id(device_id); CoTaskMemFree(device_id);
        ComPtr<IAudioClient> client;
        check(device->Activate(__uuidof(IAudioClient), CLSCTX_ALL, nullptr, reinterpret_cast<void**>(client.GetAddressOf())), "Activate audio capture", source);
        CaptureEvent ready;
        if (!ready.value) throw std::runtime_error("Create audio capture event failed");
        MixFormat mix; check(client->GetMixFormat(&mix.value), "Read audio format");
        auto* wave = mix.value;
        bool floating = wave->wFormatTag == WAVE_FORMAT_IEEE_FLOAT;
        if (wave->wFormatTag == WAVE_FORMAT_EXTENSIBLE && wave->cbSize >= 22) {
            auto* extended = reinterpret_cast<WAVEFORMATEXTENSIBLE*>(wave);
            floating = IsEqualGUID(extended->SubFormat, KSDATAFORMAT_SUBTYPE_IEEE_FLOAT);
            if (!floating && !IsEqualGUID(extended->SubFormat, KSDATAFORMAT_SUBTYPE_PCM))
                throw std::runtime_error("Windows device uses an unsupported compressed audio format");
        } else if (!floating && wave->wFormatTag != WAVE_FORMAT_PCM)
            throw std::runtime_error("Windows device uses an unsupported audio format");
        titen::Format format{wave->nSamplesPerSec, wave->nChannels, wave->wBitsPerSample, floating};
        if (format.channels < 1 || format.channels > 8 || format.rate < 8000 || format.rate > 192000 ||
            wave->nBlockAlign != format.channels * (format.bits / 8))
            throw std::runtime_error("Windows device format requires 8-192 kHz, 1-8 channel PCM");
        const uint8_t zero[8]{}; (void)titen::sample(zero, format);
        DWORD options = AUDCLNT_STREAMFLAGS_EVENTCALLBACK;
        if (source == 2) options |= AUDCLNT_STREAMFLAGS_LOOPBACK;
        check(client->Initialize(AUDCLNT_SHAREMODE_SHARED, options, 0, 0, wave, nullptr), "Initialize shared audio capture", source);
        check(client->SetEventHandle(ready.value), "Set audio capture event", source);
        ComPtr<IAudioCaptureClient> input;
        check(client->GetService(IID_PPV_ARGS(input.GetAddressOf())), "Open capture buffer", source);
        check(client->Start(), "Start audio capture", source); StartedClient started{client.Get()};
        ULONGLONG next_device_check = GetTickCount64() + 1000;
        while (!stopped) {
            const DWORD wait = WaitForSingleObject(ready.value, 1000);
            if (wait == WAIT_FAILED) check(HRESULT_FROM_WIN32(GetLastError()), "Wait for audio capture event", source);
            if ((source == 2 || microphone.empty()) && GetTickCount64() >= next_device_check) {
                ComPtr<IMMDevice> current;
                check(devices->GetDefaultAudioEndpoint(source == 1 ? eCapture : eRender, eConsole, current.GetAddressOf()), "Check default audio device");
                LPWSTR current_id = nullptr;
                check(current->GetId(&current_id), "Read default audio device identifier");
                const bool changed = active_id != current_id; CoTaskMemFree(current_id);
                if (changed) throw std::runtime_error("Default audio device changed. Start recording again to use the new device.");
                next_device_check = GetTickCount64() + 1000;
            }
            if (stopped || wait != WAIT_OBJECT_0) continue;
            UINT32 available = 0; check(input->GetNextPacketSize(&available), "Poll audio device");
            if (!available) continue;
            while (available && !stopped) {
                BYTE* data = nullptr; UINT32 frames = 0; DWORD flags = 0; UINT64 position = 0, qpc = 0;
                check(input->GetBuffer(&data, &frames, &flags, &position, &qpc), "Read audio device");
                try {
                    if (flags & AUDCLNT_BUFFERFLAGS_TIMESTAMP_ERROR) {
                        LARGE_INTEGER now, frequency; QueryPerformanceCounter(&now); QueryPerformanceFrequency(&frequency);
                        qpc = static_cast<uint64_t>((static_cast<long double>(now.QuadPart) * 10000000) / frequency.QuadPart);
                    }
                    if (flags & AUDCLNT_BUFFERFLAGS_DATA_DISCONTINUITY) report("Audio device reported a capture discontinuity. Some audio may be missing.");
                    for (uint32_t offset = 0; offset < frames; offset += 4800) {
                        uint32_t size = std::min(uint32_t(4800), frames - offset);
                        const bool silent = (flags & AUDCLNT_BUFFERFLAGS_SILENT) != 0;
                        writer.push(titen::packet(source, qpc * 100 + static_cast<uint64_t>(offset) * 1000000000 / format.rate,
                            format, silent ? nullptr : data + static_cast<size_t>(offset) * wave->nBlockAlign, size, silent));
                    }
                } catch (...) { input->ReleaseBuffer(frames); throw; }
                check(input->ReleaseBuffer(frames), "Release audio buffer");
                check(input->GetNextPacketSize(&available), "Poll audio device");
            }
        }
    } catch (const std::exception& error) { report(error.what()); failed = true; stopped = true; }
}
void watch_control() {
    HANDLE input = GetStdHandle(STD_INPUT_HANDLE);
    const DWORD type = GetFileType(input);
    while (!stopped) {
        if (type == FILE_TYPE_CHAR) {
            if (WaitForSingleObject(input, 25) != WAIT_OBJECT_0) continue;
            INPUT_RECORD event{}; DWORD read = 0;
            if (!ReadConsoleInputW(input, &event, 1, &read)) break;
            if (event.EventType == KEY_EVENT && event.Event.KeyEvent.bKeyDown &&
                (event.Event.KeyEvent.wVirtualKeyCode == VK_RETURN || event.Event.KeyEvent.uChar.UnicodeChar == 4)) break;
            continue;
        }
        if (type == FILE_TYPE_PIPE) {
            DWORD available = 0;
            if (!PeekNamedPipe(input, nullptr, 0, nullptr, &available, nullptr)) break;
            if (available == 0) { Sleep(10); continue; }
        } else if (type != FILE_TYPE_DISK) break;
        char value = 0; DWORD read = 0;
        if (!ReadFile(input, &value, 1, &read, nullptr) || read == 0 || value == '\n') break;
    }
    stopped = true;
}
BOOL WINAPI console_stop(DWORD) { stopped = true; return TRUE; }
}
int wmain(int argc, wchar_t** argv) {
    try {
        bool mic = true, system = true; std::wstring microphone;
        for (int i = 1; i < argc; ++i) {
            std::wstring argument(argv[i]);
            if (argument == L"--self-test" && argc == 2) return titen::self_test();
            if (argument == L"--list-devices" && argc == 2) { list_devices(); return 0; }
            if (argument == L"--microphone-only") system = false;
            else if (argument == L"--system-only") mic = false;
            else if (argument == L"--microphone" && i + 1 < argc) microphone = argv[++i];
            else throw std::runtime_error("Usage: titen-audio [--microphone-only | --system-only] [--microphone endpoint-id] | --list-devices | --self-test");
        }
        if (!mic && !system) throw std::runtime_error("Choose one capture mode");
        _setmode(_fileno(stdout), _O_BINARY); SetConsoleCtrlHandler(console_stop, TRUE);
        Writer writer;
        std::thread output([&] { writer.run(); });
        std::thread control(watch_control);
        std::thread microphone_thread, system_thread;
        if (mic) microphone_thread = std::thread(capture, 1, microphone, std::ref(writer));
        if (system) system_thread = std::thread(capture, 2, std::wstring{}, std::ref(writer));
        if (microphone_thread.joinable()) microphone_thread.join();
        if (system_thread.joinable()) system_thread.join();
        control.join();
        writer.close(); output.join();
        return failed ? 1 : 0;
    } catch (const std::exception& error) { std::cerr << error.what() << std::endl; return 1; }
}
#else
int main(int argc, char** argv) {
    try {
        if (argc == 2 && std::string(argv[1]) == "--self-test") return titen::self_test();
        std::cerr << "WASAPI capture requires Windows. Only --self-test is portable.\n"; return 1;
    } catch (const std::exception& error) { std::cerr << error.what() << std::endl; return 1; }
}
#endif
