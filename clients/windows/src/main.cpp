// kitchensink's Windows dashboard: a window drawn with Dear ImGui on
// Direct3D 11, and a tray icon that shows notifications and keeps the app
// running when the window is closed.
#include <windows.h>
#include <d3d11.h>
#include <dwmapi.h>
#include <shellapi.h>
#include <windowsx.h>

#include <algorithm>
#include <memory>

#include "app.h"
#include "imgui.h"
#include "imgui_impl_dx11.h"
#include "imgui_impl_win32.h"
#include "resource.h"

extern IMGUI_IMPL_API LRESULT ImGui_ImplWin32_WndProcHandler(HWND hWnd, UINT msg, WPARAM wParam, LPARAM lParam);

namespace {

constexpr UINT WM_APP_WAKE = WM_APP + 1;
constexpr UINT WM_APP_TRAY = WM_APP + 2;
constexpr UINT kTrayID = 1;
constexpr UINT kMenuOpen = 1, kMenuQuit = 2;
const wchar_t* kClassName = L"kitchensink";

ID3D11Device* device = nullptr;
ID3D11DeviceContext* context = nullptr;
IDXGISwapChain* swapChain = nullptr;
ID3D11RenderTargetView* target = nullptr;
UINT resizeW = 0, resizeH = 0;

HWND hwnd = nullptr;
UINT taskbarCreated = 0;
std::unique_ptr<ks::App> app;
bool quitting = false;
int framesWanted = 2;

void createTarget() {
    ID3D11Texture2D* back = nullptr;
    swapChain->GetBuffer(0, IID_PPV_ARGS(&back));
    device->CreateRenderTargetView(back, nullptr, &target);
    back->Release();
}

void releaseTarget() {
    if (target) target->Release();
    target = nullptr;
}

bool createDevice() {
    DXGI_SWAP_CHAIN_DESC sd{};
    sd.BufferCount = 2;
    sd.BufferDesc.Format = DXGI_FORMAT_R8G8B8A8_UNORM;
    sd.BufferUsage = DXGI_USAGE_RENDER_TARGET_OUTPUT;
    sd.OutputWindow = hwnd;
    sd.SampleDesc.Count = 1;
    sd.Windowed = TRUE;
    sd.SwapEffect = DXGI_SWAP_EFFECT_FLIP_DISCARD;
    const D3D_FEATURE_LEVEL levels[] = {D3D_FEATURE_LEVEL_11_0, D3D_FEATURE_LEVEL_10_0};
    HRESULT hr = D3D11CreateDeviceAndSwapChain(nullptr, D3D_DRIVER_TYPE_HARDWARE, nullptr, 0, levels, 2,
                                               D3D11_SDK_VERSION, &sd, &swapChain, &device, nullptr, &context);
    if (hr == DXGI_ERROR_UNSUPPORTED)  // no GPU, e.g. some remote sessions
        hr = D3D11CreateDeviceAndSwapChain(nullptr, D3D_DRIVER_TYPE_WARP, nullptr, 0, levels, 2, D3D11_SDK_VERSION,
                                           &sd, &swapChain, &device, nullptr, &context);
    if (FAILED(hr)) return false;
    createTarget();
    return true;
}

void destroyDevice() {
    releaseTarget();
    if (swapChain) swapChain->Release();
    if (context) context->Release();
    if (device) device->Release();
}

// renderFrame draws one frame, after resizing the swap chain if the window
// has changed size. Without present, it only lays out, so sizes measured
// this frame are right in the next.
void renderFrame(bool present) {
    static bool rendering = false;  // WM_SIZE can arrive while drawing
    if (rendering || !app) return;
    rendering = true;
    if (resizeW != 0) {
        releaseTarget();
        swapChain->ResizeBuffers(0, resizeW, resizeH, DXGI_FORMAT_UNKNOWN, 0);
        resizeW = resizeH = 0;
        createTarget();
    }
    ImGui_ImplDX11_NewFrame();
    ImGui_ImplWin32_NewFrame();
    ImGui::NewFrame();
    app->Frame();
    ImGui::Render();
    if (present) {
        const ImVec4& bg = ks::Colours().bg;
        const float clear[4] = {bg.x, bg.y, bg.z, 1};
        context->OMSetRenderTargets(1, &target, nullptr);
        context->ClearRenderTargetView(target, clear);
        ImGui_ImplDX11_RenderDrawData(ImGui::GetDrawData());
        swapChain->Present(1, 0);
    }
    rendering = false;
}

void setDarkTitleBar(bool dark) {
    BOOL v = dark;
    DwmSetWindowAttribute(hwnd, 20 /* DWMWA_USE_IMMERSIVE_DARK_MODE */, &v, sizeof(v));
}

void applyTheme() {
    bool dark = ks::SystemUsesDarkTheme();
    setDarkTitleBar(dark);
    app->SetDark(dark);
}

void addTrayIcon() {
    NOTIFYICONDATAW nid{sizeof(nid)};
    nid.hWnd = hwnd;
    nid.uID = kTrayID;
    nid.uFlags = NIF_ICON | NIF_MESSAGE | NIF_TIP | NIF_SHOWTIP;
    nid.uCallbackMessage = WM_APP_TRAY;
    nid.hIcon = (HICON)LoadImageW(GetModuleHandleW(nullptr), MAKEINTRESOURCEW(IDI_APP), IMAGE_ICON,
                                  GetSystemMetrics(SM_CXSMICON), GetSystemMetrics(SM_CYSMICON), 0);
    wcscpy_s(nid.szTip, L"kitchensink");
    Shell_NotifyIconW(NIM_ADD, &nid);
    nid.uVersion = NOTIFYICON_VERSION_4;
    Shell_NotifyIconW(NIM_SETVERSION, &nid);
}

void removeTrayIcon() {
    NOTIFYICONDATAW nid{sizeof(nid)};
    nid.hWnd = hwnd;
    nid.uID = kTrayID;
    Shell_NotifyIconW(NIM_DELETE, &nid);
}

// truncate shortens text to fit a fixed-size buffer of n wide characters.
std::wstring truncate(const std::string& s, size_t n) {
    std::wstring w = ks::Widen(s);
    if (w.size() >= n) w = w.substr(0, n - 2) + L"…";
    return w;
}

void showNotification(const std::string& title, const std::string& body) {
    NOTIFYICONDATAW nid{sizeof(nid)};
    nid.hWnd = hwnd;
    nid.uID = kTrayID;
    nid.uFlags = NIF_INFO;
    nid.dwInfoFlags = NIIF_USER | NIIF_LARGE_ICON;
    nid.hBalloonIcon = (HICON)LoadImageW(GetModuleHandleW(nullptr), MAKEINTRESOURCEW(IDI_APP), IMAGE_ICON,
                                         GetSystemMetrics(SM_CXICON), GetSystemMetrics(SM_CYICON), 0);
    wcscpy_s(nid.szInfoTitle, truncate(title, ARRAYSIZE(nid.szInfoTitle)).c_str());
    wcscpy_s(nid.szInfo, truncate(body.empty() ? " " : body, ARRAYSIZE(nid.szInfo)).c_str());
    Shell_NotifyIconW(NIM_MODIFY, &nid);
}

void showWindow() {
    ShowWindow(hwnd, IsIconic(hwnd) ? SW_RESTORE : SW_SHOW);
    SetForegroundWindow(hwnd);
    framesWanted = 3;
}

void trayMenu() {
    HMENU menu = CreatePopupMenu();
    AppendMenuW(menu, MF_STRING, kMenuOpen, L"Open kitchensink");
    AppendMenuW(menu, MF_SEPARATOR, 0, nullptr);
    AppendMenuW(menu, MF_STRING, kMenuQuit, L"Quit");
    SetMenuDefaultItem(menu, kMenuOpen, FALSE);
    POINT pt;
    GetCursorPos(&pt);
    SetForegroundWindow(hwnd);  // so the menu closes when clicking away
    UINT cmd = TrackPopupMenu(menu, TPM_RETURNCMD | TPM_RIGHTBUTTON, pt.x, pt.y, 0, hwnd, nullptr);
    DestroyMenu(menu);
    if (cmd == kMenuOpen) showWindow();
    if (cmd == kMenuQuit) {
        quitting = true;
        DestroyWindow(hwnd);
    }
}

// hitTest finds what part of the window a point is over. The standard
// resize borders on the sides and bottom stay; the top one sits inside the
// title bar the app draws.
LRESULT hitTest(HWND h, WPARAM wParam, LPARAM lParam) {
    LRESULT hit = DefWindowProcW(h, WM_NCHITTEST, wParam, lParam);
    if (hit != HTCLIENT) return hit;
    POINT pt{GET_X_LPARAM(lParam), GET_Y_LPARAM(lParam)};
    ScreenToClient(h, &pt);
    if (!IsZoomed(h)) {
        UINT dpi = GetDpiForWindow(h);
        int band = GetSystemMetricsForDpi(SM_CYFRAME, dpi);
        if (pt.y < band) {
            RECT rc;
            GetClientRect(h, &rc);
            int corner = band + GetSystemMetricsForDpi(SM_CXPADDEDBORDER, dpi) * 2;
            if (pt.x < corner) return HTTOPLEFT;
            if (pt.x >= rc.right - corner) return HTTOPRIGHT;
            return HTTOP;
        }
    }
    if (app && app->InCaption(float(pt.x), float(pt.y))) return HTCAPTION;
    return HTCLIENT;
}

LRESULT CALLBACK wndProc(HWND h, UINT msg, WPARAM wParam, LPARAM lParam) {
    if (ImGui_ImplWin32_WndProcHandler(h, msg, wParam, lParam)) return true;
    if (msg == taskbarCreated && taskbarCreated) {
        addTrayIcon();  // Explorer restarted
        return 0;
    }
    switch (msg) {
    case WM_NCCALCSIZE:
        if (wParam) {
            // Keep the standard frame except the title bar, so the window
            // still resizes, snaps and has a shadow like any other.
            auto* params = reinterpret_cast<NCCALCSIZE_PARAMS*>(lParam);
            RECT before = params->rgrc[0];
            DefWindowProcW(h, msg, wParam, lParam);
            // A maximised window hangs over the screen edges by its frame
            // width, so the top needs the same inset as the sides.
            params->rgrc[0].top = before.top + (IsZoomed(h) ? params->rgrc[0].left - before.left : 0);
            return 0;
        }
        break;
    case WM_NCHITTEST: return hitTest(h, wParam, lParam);
    case WM_SIZE:
        if (wParam != SIZE_MINIMIZED) {
            resizeW = LOWORD(lParam);
            resizeH = HIWORD(lParam);
            // While the window is being dragged to a new size, Windows runs
            // its own message loop and ours waits, so draw from here; else
            // the last frame is stretched until the drag ends. Lay out once
            // first so the new size is measured.
            if (IsWindowVisible(h)) {
                renderFrame(false);
                renderFrame(true);
            }
        }
        return 0;
    case WM_GETMINMAXINFO: {
        float scale = ImGui_ImplWin32_GetDpiScaleForHwnd(h);
        auto* mmi = reinterpret_cast<MINMAXINFO*>(lParam);
        mmi->ptMinTrackSize = {LONG(480 * scale), LONG(360 * scale)};
        return 0;
    }
    case WM_DPICHANGED: {
        if (app) {
            app->SetScale(float(HIWORD(wParam)) / 96.0f);
            ImGui_ImplDX11_InvalidateDeviceObjects();
        }
        const RECT* r = reinterpret_cast<RECT*>(lParam);
        SetWindowPos(h, nullptr, r->left, r->top, r->right - r->left, r->bottom - r->top,
                     SWP_NOZORDER | SWP_NOACTIVATE);
        return 0;
    }
    case WM_SETTINGCHANGE:
        if (lParam && wcscmp(reinterpret_cast<const wchar_t*>(lParam), L"ImmersiveColorSet") == 0 && app) applyTheme();
        return 0;
    case WM_CLOSE:
        // Closing the window keeps the app in the tray; Quit is in the
        // tray menu.
        ShowWindow(h, SW_HIDE);
        return 0;
    case WM_APP_TRAY:
        switch (LOWORD(lParam)) {
        case WM_LBUTTONUP:
        case NIN_SELECT:
        case NIN_KEYSELECT: showWindow(); break;
        case WM_RBUTTONUP:
        case WM_CONTEXTMENU: trayMenu(); break;
        case NIN_BALLOONUSERCLICK:
            if (!app || !app->NotificationClicked()) showWindow();
            break;
        }
        return 0;
    case WM_ENDSESSION:
        if (wParam) removeTrayIcon();  // Windows is shutting down or signing out
        return 0;
    case WM_DESTROY:
        removeTrayIcon();
        PostQuitMessage(0);
        return 0;
    }
    return DefWindowProcW(h, msg, wParam, lParam);
}

}  // namespace

int WINAPI wWinMain(HINSTANCE instance, HINSTANCE, PWSTR cmdLine, int) {
    // One instance: a second launch shows the first one's window.
    HANDLE mutex = CreateMutexW(nullptr, TRUE, L"Local\\kitchensink-dashboard");
    if (GetLastError() == ERROR_ALREADY_EXISTS) {
        if (HWND other = FindWindowW(kClassName, nullptr)) {
            ShowWindow(other, SW_SHOW);
            ShowWindow(other, SW_RESTORE);
            SetForegroundWindow(other);
        }
        return 0;
    }
    bool startHidden = wcsstr(cmdLine, L"--tray") != nullptr;

    ImGui_ImplWin32_EnableDpiAwareness();
    CoInitializeEx(nullptr, COINIT_APARTMENTTHREADED);

    WNDCLASSEXW wc{sizeof(wc)};
    wc.style = CS_CLASSDC;
    wc.lpfnWndProc = wndProc;
    wc.hInstance = instance;
    wc.hIcon = LoadIconW(instance, MAKEINTRESOURCEW(IDI_APP));
    wc.hIconSm = (HICON)LoadImageW(instance, MAKEINTRESOURCEW(IDI_APP), IMAGE_ICON, GetSystemMetrics(SM_CXSMICON),
                                   GetSystemMetrics(SM_CYSMICON), 0);
    wc.hCursor = LoadCursorW(nullptr, IDC_ARROW);
    wc.lpszClassName = kClassName;
    RegisterClassExW(&wc);

    float scale = ImGui_ImplWin32_GetDpiScaleForMonitor(MonitorFromPoint(POINT{0, 0}, MONITOR_DEFAULTTOPRIMARY));
    hwnd = CreateWindowExW(0, kClassName, L"kitchensink", WS_OVERLAPPEDWINDOW, CW_USEDEFAULT, CW_USEDEFAULT,
                           int(1360 * scale), int(860 * scale), nullptr, nullptr, instance, nullptr);
    // Recalculate the frame now WM_NCCALCSIZE has removed the title bar.
    SetWindowPos(hwnd, nullptr, 0, 0, 0, 0, SWP_FRAMECHANGED | SWP_NOMOVE | SWP_NOSIZE | SWP_NOZORDER);
    taskbarCreated = RegisterWindowMessageW(L"TaskbarCreated");
    if (!createDevice()) {
        MessageBoxW(nullptr, L"Couldn't start Direct3D 11.", L"kitchensink", MB_ICONERROR);
        return 1;
    }

    IMGUI_CHECKVERSION();
    ImGui::CreateContext();
    ImGuiIO& io = ImGui::GetIO();
    io.IniFilename = nullptr;
    io.ConfigFlags |= ImGuiConfigFlags_NavEnableKeyboard;
    ImGui_ImplWin32_Init(hwnd);
    ImGui_ImplDX11_Init(device, context);

    app = std::make_unique<ks::App>(
        hwnd, device, [] { PostMessageW(hwnd, WM_APP_WAKE, 0, 0); }, showNotification);
    app->SetScale(ImGui_ImplWin32_GetDpiScaleForHwnd(hwnd));
    applyTheme();
    addTrayIcon();
    if (!startHidden) {
        ShowWindow(hwnd, SW_SHOWDEFAULT);
        UpdateWindow(hwnd);
    }

    // Draw only when something happens: input, data from the server, or
    // once a second so "updated 3m ago" stays current; more often while the
    // cat's moving.
    bool running = true;
    while (running) {
        bool visible = IsWindowVisible(hwnd) && !IsIconic(hwnd);
        DWORD timeout = !visible                               ? INFINITE
                        : (framesWanted > 0 || app->Busy()) ? 0
                        : app->FrameInterval() > 0          ? app->FrameInterval()
                                                            : 1000;
        timeout = std::min<DWORD>(timeout, app->FlushIn());
        MsgWaitForMultipleObjectsEx(0, nullptr, timeout, QS_ALLINPUT, MWMO_INPUTAVAILABLE);
        MSG msg;
        while (PeekMessageW(&msg, nullptr, 0, 0, PM_REMOVE)) {
            if (msg.message == WM_QUIT) running = false;
            TranslateMessage(&msg);
            DispatchMessageW(&msg);
            // ImGui needs a couple of frames to settle after input.
            framesWanted = 2;
        }
        if (!running) break;
        app->Pump();
        if (!IsWindowVisible(hwnd) || IsIconic(hwnd)) continue;

        renderFrame(true);
        if (framesWanted > 0) framesWanted--;
    }

    app.reset();
    ImGui_ImplDX11_Shutdown();
    ImGui_ImplWin32_Shutdown();
    ImGui::DestroyContext();
    destroyDevice();
    CoUninitialize();
    ReleaseMutex(mutex);
    return 0;
}
