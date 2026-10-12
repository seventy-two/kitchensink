#include "config.h"

#include <windows.h>
#include <dpapi.h>
#include <shlobj.h>
#include <wincrypt.h>

#include <fstream>
#include <sstream>

#include "net.h"

namespace ks {

namespace {

const wchar_t* kRunKey = L"Software\\Microsoft\\Windows\\CurrentVersion\\Run";
const wchar_t* kRunValue = L"kitchensink";

std::wstring configPath(bool create) {
    PWSTR appData = nullptr;
    if (FAILED(SHGetKnownFolderPath(FOLDERID_RoamingAppData, 0, nullptr, &appData))) return {};
    std::wstring dir = std::wstring(appData) + L"\\kitchensink";
    CoTaskMemFree(appData);
    if (create) CreateDirectoryW(dir.c_str(), nullptr);
    return dir + L"\\config.json";
}

std::string protect(const std::string& plain) {
    DATA_BLOB in{DWORD(plain.size()), (BYTE*)plain.data()}, out{};
    if (!CryptProtectData(&in, L"kitchensink token", nullptr, nullptr, nullptr, CRYPTPROTECT_UI_FORBIDDEN, &out))
        return {};
    DWORD n = 0;
    CryptBinaryToStringA(out.pbData, out.cbData, CRYPT_STRING_BASE64 | CRYPT_STRING_NOCRLF, nullptr, &n);
    std::string b64(n, '\0');
    CryptBinaryToStringA(out.pbData, out.cbData, CRYPT_STRING_BASE64 | CRYPT_STRING_NOCRLF, b64.data(), &n);
    b64.resize(n);
    LocalFree(out.pbData);
    return b64;
}

std::string unprotect(const std::string& b64) {
    DWORD n = 0;
    if (!CryptStringToBinaryA(b64.c_str(), DWORD(b64.size()), CRYPT_STRING_BASE64, nullptr, &n, nullptr, nullptr))
        return {};
    std::string raw(n, '\0');
    CryptStringToBinaryA(b64.c_str(), DWORD(b64.size()), CRYPT_STRING_BASE64, (BYTE*)raw.data(), &n, nullptr, nullptr);
    DATA_BLOB in{n, (BYTE*)raw.data()}, out{};
    if (!CryptUnprotectData(&in, nullptr, nullptr, nullptr, nullptr, CRYPTPROTECT_UI_FORBIDDEN, &out)) return {};
    std::string plain((char*)out.pbData, out.cbData);
    LocalFree(out.pbData);
    return plain;
}

const char* priorityName(Priority p) {
    switch (p) {
    case Priority::Low: return "low";
    case Priority::High: return "high";
    default: return "normal";
    }
}

}  // namespace

Config LoadConfig() {
    Config c;
    std::ifstream f(configPath(false), std::ios::binary);
    if (!f) return c;
    std::stringstream ss;
    ss << f.rdbuf();
    auto j = nlohmann::json::parse(ss.str(), nullptr, false);
    if (!j.is_object()) return c;
    c.url = j.value("url", "");
    c.token = unprotect(j.value("token", ""));
    c.minPriority = ParsePriority(j.value("minPriority", "normal"));
    c.notify = j.value("notify", true);
    // "oled" was once one switch for all of OLED care.
    if (auto it = j.find("oled"); it != j.end() && it->is_boolean()) {
        bool on = it->get<bool>();
        c.oled = {on, on, on, on};
    } else if (it != j.end() && it->is_object()) {
        c.oled = {it->value("shift", false), it->value("black", false), it->value("dim", false), it->value("swap", false)};
    }
    c.hidePrivate = j.value("hidePrivate", false);
    c.dancer = j.value("dancer", false);
    auto ids = [&](const char* key, std::set<std::string>& out) {
        if (auto it = j.find(key); it != j.end() && it->is_array())
            for (const auto& id : *it)
                if (id.is_string()) out.insert(id.get<std::string>());
    };
    ids("expanded", c.expanded);
    ids("collapsed", c.collapsed);
    if (auto it = j.find("order"); it != j.end() && it->is_array())
        for (const auto& id : *it)
            if (id.is_string()) c.order.push_back(id.get<std::string>());
    return c;
}

bool SaveConfig(const Config& c) {
    nlohmann::json j = {
        {"url", c.url},
        {"token", protect(c.token)},
        {"minPriority", priorityName(c.minPriority)},
        {"notify", c.notify},
        {"oled", {{"shift", c.oled.shift}, {"black", c.oled.black}, {"dim", c.oled.dim}, {"swap", c.oled.swap}}},
        {"hidePrivate", c.hidePrivate},
        {"dancer", c.dancer},
        {"expanded", c.expanded},
        {"collapsed", c.collapsed},
        {"order", c.order},
    };
    std::wstring path = configPath(true);
    std::wstring tmp = path + L".tmp";
    {
        std::ofstream f(tmp, std::ios::binary | std::ios::trunc);
        if (!f) return false;
        f << j.dump(2);
        if (!f) return false;
    }
    return MoveFileExW(tmp.c_str(), path.c_str(), MOVEFILE_REPLACE_EXISTING) != 0;
}

bool StartsWithWindows() {
    HKEY key;
    if (RegOpenKeyExW(HKEY_CURRENT_USER, kRunKey, 0, KEY_READ, &key) != ERROR_SUCCESS) return false;
    bool on = RegQueryValueExW(key, kRunValue, nullptr, nullptr, nullptr, nullptr) == ERROR_SUCCESS;
    RegCloseKey(key);
    return on;
}

void SetStartWithWindows(bool on) {
    HKEY key;
    if (RegOpenKeyExW(HKEY_CURRENT_USER, kRunKey, 0, KEY_SET_VALUE, &key) != ERROR_SUCCESS) return;
    if (on) {
        wchar_t exe[MAX_PATH];
        GetModuleFileNameW(nullptr, exe, MAX_PATH);
        std::wstring cmd = L"\"" + std::wstring(exe) + L"\" --tray";
        RegSetValueExW(key, kRunValue, 0, REG_SZ, (const BYTE*)cmd.c_str(), DWORD((cmd.size() + 1) * sizeof(wchar_t)));
    } else {
        RegDeleteValueW(key, kRunValue);
    }
    RegCloseKey(key);
}

}  // namespace ks
