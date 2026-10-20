#pragma once
#include <algorithm>
#include <cmath>
#include <cstdint>
#include <cstring>
#include <stdexcept>
#include <vector>

namespace titen {
struct Format {
    uint32_t rate;
    uint16_t channels;
    uint16_t bits;
    bool floating;
};
inline int16_t sample(const uint8_t* p, const Format& f) {
    double value = 0;
    if (f.floating && f.bits == 32) {
        float x; std::memcpy(&x, p, 4); value = x;
    } else if (f.floating && f.bits == 64) {
        double x; std::memcpy(&x, p, 8); value = x;
    } else if (!f.floating && f.bits == 8) {
        value = (int(p[0]) - 128) / 128.0;
    } else if (!f.floating && f.bits == 16) {
        int16_t x; std::memcpy(&x, p, 2); return x;
    } else if (!f.floating && f.bits == 24) {
        int32_t x = int32_t(p[0]) | (int32_t(p[1]) << 8) | (int32_t(p[2]) << 16);
        if (x & 0x800000) x |= ~0xffffff;
        value = x / 8388608.0;
    } else if (!f.floating && f.bits == 32) {
        int32_t x; std::memcpy(&x, p, 4); value = x / 2147483648.0;
    } else throw std::runtime_error("Unsupported Windows audio sample format");
    if (!std::isfinite(value)) value = 0;
    return static_cast<int16_t>(std::lround(std::clamp(value, -1.0, 1.0) * (value < 0 ? 32768 : 32767)));
}
inline void little(std::vector<uint8_t>& b, uint64_t v, int count) {
    for (int i = 0; i < count; ++i) b.push_back(static_cast<uint8_t>(v >> (i * 8)));
}
inline std::vector<uint8_t> packet(uint8_t source, uint64_t ns, const Format& f,
                                   const uint8_t* data, uint32_t frames, bool silent) {
    if (source < 1 || source > 2 || f.channels < 1 || f.channels > 8 ||
        f.rate < 8000 || f.rate > 192000 || frames == 0 || frames > 96000)
        throw std::runtime_error("Invalid Windows audio packet");
    std::vector<uint8_t> b{'T','I','T','1',source,1};
    little(b, 1, 2); little(b, f.rate, 4); little(b, ns, 8); little(b, frames * 2, 4);
    for (uint32_t i = 0; i < frames; ++i) {
        int32_t sum = 0;
        if (!silent) for (uint16_t c = 0; c < f.channels; ++c)
            sum += sample(data + (static_cast<size_t>(i) * f.channels + c) * (f.bits / 8), f);
        little(b, static_cast<uint16_t>(sum / f.channels), 2);
    }
    return b;
}
inline int self_test() {
    const float values[] = {-1, -1, 0.5f, 0.5f, 1, 1};
    const auto b = packet(2, 123456789, {48000,2,32,true}, reinterpret_cast<const uint8_t*>(values), 3, false);
    const std::vector<uint8_t> expected = {'T','I','T','1',2,1,1,0,0x80,0xbb,0,0,0x15,0xcd,0x5b,7,0,0,0,0,6,0,0,0,0,0x80,0,0x40,0xff,0x7f};
    if (b != expected) throw std::runtime_error("TIT1 float/downmix fixture failed");
    const uint8_t negative24[] = {0,0,0x80};
    if (sample(negative24, {48000,1,24,false}) != -32768) throw std::runtime_error("PCM24 fixture failed");
    const uint8_t maximum32[] = {0xff,0xff,0xff,0x7f};
    if (sample(maximum32, {48000,1,32,false}) != 32767) throw std::runtime_error("PCM32 fixture failed");
    const auto silence = packet(1, 0, {16000,1,16,false}, nullptr, 2, true);
    if (silence.size() != 28 || silence[24] || silence[25] || silence[26] || silence[27])
        throw std::runtime_error("Silent packet fixture failed");
    return 0;
}
}
