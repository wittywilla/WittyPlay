// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 WittyWilla and contributors

package main

import (
	"slices"
	"strings"
	"testing"

	"github.com/wittywilla/wittyplay/emulators"
)

// Each extractor is tested on a few lines of its emulator's real source, at the version
// WittyPlay pins, copied here as test data. They're credited by path; each project's licence
// covers its lines (named at each).

// find returns the option at a path.
func find(t *testing.T, opts []option, path ...string) option {
	t.Helper()
	i := slices.IndexFunc(opts, func(o option) bool { return slices.Equal(o.Path, path) })
	if i < 0 {
		t.Fatalf("no option at %v in %d", path, len(opts))
	}
	return opts[i]
}

func TestDolphin(t *testing.T) {
	files := map[string]string{
		// dolphin-emu/dolphin 2609b, Source/Core/Core/Config/MainSettings.cpp (GPL-2.0-or-later)
		"Source/Core/Core/Config/MainSettings.cpp": `
#if defined(ANDROID)
constexpr bool DEFAULT_CPU_THREAD = true;
#else
constexpr bool DEFAULT_CPU_THREAD = false;
#endif
const Info<bool> MAIN_CPU_THREAD{{System::Main, "Core", "CPUThread"}, DEFAULT_CPU_THREAD};
const Info<PowerPC::CPUCore> MAIN_CPU_CORE{{System::Main, "Core", "CPUCore"},
                                           PowerPC::DefaultCPUCore()};
const Info<float> MAIN_EMULATION_SPEED{{System::Main, "Core", "EmulationSpeed"}, 1.0f};
const Info<std::string> MAIN_DUMP_PATH{{System::Main, "General", "DumpPath"}, ""};
const Info<u32> MAIN_MEM1_SIZE{{System::Main, "Core", "MEM1Size"}, Memory::MEM1_SIZE_RETAIL};
#ifdef _WIN32
const Info<std::string> MAIN_WASAPI_DEVICE{{System::Main, "DSP", "WASAPIDevice"}, "Default"};
#endif
      Info<bool>{{System::Main, "Core", "AdapterRumble3"}, true}};
`,
		// dolphin-emu/dolphin 2609b, Source/Core/Core/Config/GraphicsSettings.cpp (GPL-2.0-or-later)
		"Source/Core/Core/Config/GraphicsSettings.cpp": `
const Info<bool> GFX_WIDESCREEN_HACK{{System::GFX, "Settings", "wideScreenHack"}, false};
const Info<AspectMode> GFX_ASPECT_RATIO{{System::GFX, "Settings", "AspectRatio"}, AspectMode::Auto};
const Info<float> GFX_WIDESCREEN_HEURISTIC_WIDESCREEN_RATIO{
    {System::GFX, "Settings", "WidescreenHeuristicWidescreenRatio"}, (16 / 9.f) / (4 / 3.f)};
`,
		// dolphin-emu/dolphin 2609b, Source/Core/VideoCommon/VideoConfig.h (GPL-2.0-or-later)
		"Source/Core/VideoCommon/VideoConfig.h": `
enum class AspectMode : int
{
  Auto,           // ~4:3 or ~16:9 (auto detected)
  ForceWide,      // ~16:9
  ForceStandard,  // ~4:3
};`,
		// dolphin-emu/dolphin 2609b, Source/Core/Core/PowerPC/PowerPC.h and HW/Memmap.h
		"Source/Core/Core/PowerPC/PowerPC.h": `
enum class CPUCore
{
  Interpreter = 0,
  JIT64 = 1,
  JITARM64 = 4,
  CachedInterpreter = 5,
};`,
		"Source/Core/Core/HW/Memmap.h": `constexpr u32 MEM1_SIZE_RETAIL = 0x01800000U;`,
	}
	opts, err := extractDolphin(files)
	if err != nil {
		t.Fatal(err)
	}
	if o := find(t, opts, "Core", "CPUThread"); o.Default != false || o.File != "Config/Dolphin.ini" || o.Values["true"] != "True" {
		t.Errorf("CPU thread: %+v", o)
	}
	if o := find(t, opts, "Core", "CPUCore"); o.Default != "1" || len(o.Choices) != 4 || o.Choices[1].Label != "JIT 64" {
		t.Errorf("CPU core: %+v", o)
	}
	if o := find(t, opts, "Core", "MEM1Size"); o.Default != float64(0x01800000) {
		t.Errorf("MEM1: %+v", o)
	}
	if o := find(t, opts, "Settings", "AspectRatio"); o.File != "Config/GFX.ini" || o.Default != "0" || o.Choices[1].Label != "Force wide" {
		t.Errorf("aspect: %+v", o)
	}
	if o := find(t, opts, "Settings", "WidescreenHeuristicWidescreenRatio"); o.Default != (16/9.0)/(4/3.0) {
		t.Errorf("ratio: %+v", o)
	}
	find(t, opts, "Core", "AdapterRumble3")
	if slices.ContainsFunc(opts, func(o option) bool { return o.Path[1] == "WASAPIDevice" }) {
		t.Error("a Windows-only setting")
	}
	list, _ := settings(opts, dolphinPrefix)
	i := slices.IndexFunc(list, func(s emulators.Setting) bool { return s.Label == "Dump path" })
	if i < 0 || !list[i].Fixed {
		t.Error("a path is offered")
	}
}

func TestPCSX2(t *testing.T) {
	files := map[string]string{
		// PCSX2/pcsx2 v2.8.2, pcsx2/Pcsx2Config.cpp (GPL-3.0-or-later)
		"pcsx2/Pcsx2Config.cpp": `
const char* Pcsx2Config::GSOptions::AspectRatioNames[(size_t)AspectRatioType::MaxCount + 1] = {
	"Stretch",
	"Auto 4:3/3:2",
	"4:3",
	nullptr};

Pcsx2Config::GSOptions::GSOptions()
{
	bitsets[0] = 0;
	PCRTCAntiBlur = true;
}

void Pcsx2Config::GSOptions::LoadSave(SettingsWrapper& wrap)
{
	SettingsWrapSection("EmuCore/GS");
	SettingsWrapBitBool(VsyncEnable);
	SettingsWrapBitBoolEx(PCRTCAntiBlur, "pcrtc_antiblur");
	SettingsWrapEnumEx(AspectRatio, "AspectRatio", AspectRatioNames);
	SettingsWrapIntEnumEx(Renderer, "Renderer");
	SettingsWrapEntryEx(UpscaleMultiplier, "upscale_multiplier");
	SettingsWrapEntry(VsyncQueueSize);
	SettingsWrapEntry(HWDumpDirectory);
	SyncToHostRefreshRate = wrap.EntryBitBool("EmuCore/GS", "SyncToHostRefreshRate", SyncToHostRefreshRate, SyncToHostRefreshRate);
}
`,
		// PCSX2/pcsx2 v2.8.2, pcsx2/Config.h (GPL-3.0-or-later)
		"pcsx2/Config.h": `
enum class GSRendererType : s8
{
	Auto = -1,
	DX11 = 3,
	OGL = 12,
	VK = 14,
};
enum class AspectRatioType : u8
{
	Stretch,
	RAuto4_3_3_2,
	R4_3,
	MaxCount
};
	struct GSOptions
	{
		static constexpr float DEFAULT_UPSCALE_MULTIPLIER = 1.0f;
		static constexpr AspectRatioType DEFAULT_ASPECT_RATIO = AspectRatioType::RAuto4_3_3_2;
		union
		{
			u64 bitsets[2];
			struct
			{
				bool
					VsyncEnable : 1,
					PCRTCAntiBlur : 1,
					SyncToHostRefreshRate : 1;
			};
		};
		int VsyncQueueSize = 2;
		AspectRatioType AspectRatio = DEFAULT_ASPECT_RATIO;
		GSRendererType Renderer = GSRendererType::Auto;
		float UpscaleMultiplier = DEFAULT_UPSCALE_MULTIPLIER;
		std::string HWDumpDirectory;
	};`,
	}
	opts, err := extractPCSX2(files)
	if err != nil {
		t.Fatal(err)
	}
	if o := find(t, opts, "EmuCore/GS", "VsyncEnable"); o.Type != "bool" || o.Default != false || o.File != "PCSX2/inis/PCSX2.ini" {
		t.Errorf("vsync: %+v", o)
	}
	if o := find(t, opts, "EmuCore/GS", "pcrtc_antiblur"); o.Default != true {
		t.Errorf("anti-blur: %+v", o)
	}
	if o := find(t, opts, "EmuCore/GS", "AspectRatio"); o.Default != "Auto 4:3/3:2" || len(o.Choices) != 3 {
		t.Errorf("aspect: %+v", o)
	}
	if o := find(t, opts, "EmuCore/GS", "Renderer"); o.Default != "-1" || len(o.Choices) != 4 {
		t.Errorf("renderer: %+v", o)
	}
	if o := find(t, opts, "EmuCore/GS", "upscale_multiplier"); o.Type != "float" || o.Default != 1.0 {
		t.Errorf("upscale: %+v", o)
	}
	if o := find(t, opts, "EmuCore/GS", "VsyncQueueSize"); o.Default != 2.0 {
		t.Errorf("queue: %+v", o)
	}
	find(t, opts, "EmuCore/GS", "SyncToHostRefreshRate")
	find(t, opts, "EmuCore/GS", "HWDumpDirectory")
}

func TestPPSSPP(t *testing.T) {
	files := map[string]string{
		// hrydgard/ppsspp v1.20.4, Core/Config.cpp (GPL-2.0-or-later)
		"Core/Config.cpp": `
static bool DefaultCodeGen() {
#if PPSSPP_ARCH(ARM) || PPSSPP_ARCH(ARM64) || PPSSPP_ARCH(X86) || PPSSPP_ARCH(AMD64)
	return true;
#else
	return false;
#endif
}
static const ConfigSetting graphicsSettings[] = {
	ConfigSetting("FrameSkip", SETTING(g_Config, iFrameSkip), 0, CfgFlag::PER_GAME),
	ConfigSetting("InternalResolution", SETTING(g_Config, iInternalResolution), 2, CfgFlag::PER_GAME | CfgFlag::REPORT),
	ConfigSetting("UberShaderVertex", SETTING(g_Config, bUberShaderVertex), &DefaultCodeGen, CfgFlag::DEFAULT),
	ConfigSetting("GraphicsBackend", SETTING(g_Config, iGPUBackend), &DefaultGPUBackend, &GPUBackendTranslator::To, &GPUBackendTranslator::From, CfgFlag::DEFAULT | CfgFlag::REPORT),
};
static const ConfigSetting soundSettings[] = {
	ConfigSetting("GlobalVolume", SETTING(g_Config, iLegacyGameVolume), VOLUME_FULL, CfgFlag::PER_GAME | CfgFlag::DONT_SAVE),
	ConfigSetting("AudioBackend", SETTING(g_Config, iAudioBackend), 0, CfgFlag::PER_GAME),
	ConfigSetting("FastForwardSpeed", SETTING(g_Config, fFastForwardSpeed), 1.0f, CfgFlag::PER_GAME),
};
static const ConfigSetting touchControlSettings[] = {
	ConfigSetting("ShowTouchCross", SETTING(g_Config.touchControlsLandscape, bShowTouchCross), true, CfgFlag::PER_GAME),
};
static const ConfigSectionMeta g_sectionMeta[] = {
	{ &g_Config, graphicsSettings, ARRAY_SIZE(graphicsSettings), "Graphics" },
	{ &g_Config, soundSettings, ARRAY_SIZE(soundSettings), "Sound" },
	{ &g_Config.touchControlsLandscape, touchControlSettings, ARRAY_SIZE(touchControlSettings), "TouchControls.Landscape", "Control" },
};`,
		// hrydgard/ppsspp v1.20.4, Core/ConfigValues.h (GPL-2.0-or-later)
		"Core/ConfigValues.h": `constexpr int VOLUME_FULL = 10;`,
	}
	opts, err := extractPPSSPP(files)
	if err != nil {
		t.Fatal(err)
	}
	if o := find(t, opts, "Graphics", "InternalResolution"); o.Default != 2.0 || o.File != "PSP/SYSTEM/ppsspp.ini" {
		t.Errorf("resolution: %+v", o)
	}
	if o := find(t, opts, "Graphics", "UberShaderVertex"); o.Default != true || o.Values["false"] != "False" {
		t.Errorf("uber shader: %+v", o)
	}
	if o := find(t, opts, "Sound", "FastForwardSpeed"); o.Type != "float" || o.Default != 1.0 {
		t.Errorf("speed: %+v", o)
	}
	for _, o := range opts {
		switch o.Path[1] {
		case "GlobalVolume", "GraphicsBackend", "ShowTouchCross":
			t.Errorf("%v was read: never saved, saved by a translator, or input", o.Path)
		}
	}
}

func TestMelonDS(t *testing.T) {
	files := map[string]string{
		// melonDS-emu/melonDS 1.1, src/frontend/qt_sdl/Config.cpp (GPL-3.0-or-later)
		"src/frontend/qt_sdl/Config.cpp": `
DefaultList<int> DefaultInts =
{
    {"Instance*.Keyboard", -1},
    {"3D.Renderer", renderer3D_Software},
    {"Instance*.Audio.Volume", 256},
};
RangeList IntRanges =
{
    {"3D.Renderer", {0, renderer3D_Max-1}},
    {"Instance*.Audio.Volume", {0, 256}},
};
DefaultList<bool> DefaultBools =
{
    {"3D.Soft.Threaded", true},
};
DefaultList<double> DefaultDoubles =
{
    {"TargetFPS", 60.0},
};
LegacyEntry LegacyFile[] =
{
    {"Key_A",      0, "Keyboard.A", true},
    {"ScreenSwap",     1, "Window0.ScreenSwap", true},
    {"BIOS9Path", 2, "DS.BIOS9Path", false},
    {"RecentROM_0", 4, "RecentROM[0]", false},
    {"", -1, "", false}
};`,
		// melonDS-emu/melonDS 1.1, src/frontend/qt_sdl/EmuInstance.h (GPL-3.0-or-later)
		"src/frontend/qt_sdl/EmuInstance.h": `
enum
{
    renderer3D_Software = 0,
#ifdef OGLRENDERER_ENABLED
    renderer3D_OpenGL,
    renderer3D_OpenGLCompute,
#endif
    renderer3D_Max,
};`,
	}
	opts, err := extractMelonDS(files)
	if err != nil {
		t.Fatal(err)
	}
	if o := find(t, opts, "3D", "Renderer"); o.Type != "enum" || o.Default != "0" || len(o.Choices) != 3 || o.Choices[1].Label != "Open GL" || o.File != "melonDS.toml" {
		t.Errorf("renderer: %+v", o)
	}
	if o := find(t, opts, "Instance0", "Audio", "Volume"); o.Default != 256.0 || o.Max != 256 {
		t.Errorf("volume: %+v", o)
	}
	if o := find(t, opts, "Instance0", "Window0", "ScreenSwap"); o.Type != "bool" || o.Default != false {
		t.Errorf("swap: %+v", o)
	}
	if o := find(t, opts, "TargetFPS"); o.Type != "float" || o.Default != 60.0 {
		t.Errorf("fps: %+v", o)
	}
	find(t, opts, "3D", "Soft", "Threaded")
	find(t, opts, "DS", "BIOS9Path")
	for _, o := range opts {
		if strings.Contains(strings.Join(o.Path, "."), "Keyboard") || strings.Contains(o.Path[0], "Recent") {
			t.Errorf("%v was read", o.Path)
		}
	}
}

func TestFlycast(t *testing.T) {
	files := map[string]string{
		// flyinghead/flycast v2.7, core/cfg/option.cpp (GPL-2.0-only)
		"core/cfg/option.cpp": `
Option<bool> Widescreen("rend.WideScreen");
Option<int> AudioBufferSize("aica.BufferSize", 2822);	// 64 ms
#ifdef __APPLE__
Option<bool> DelayFrameSwapping("rend.DelayFrameSwapping", false);
#else
Option<bool> DelayFrameSwapping("rend.DelayFrameSwapping", true);
#endif
Option<int64_t> PixelBufferSize("rend.PixelBufferSize", 512_MB);
Option<float> ProfilerFrameWarningTime("Profiler.FrameWarningTime", 1.0f / 55.0f);
OptionString DNS("DNS", "dns.flyca.st", "network");
Option<int> MouseSensitivity("MouseSensitivity", 100, "input");
std::array<Option<int>, 4> CrosshairColor {
	Option<int>("rend.CrossHairColor1"),
};`,
		// flyinghead/flycast v2.7, core/cfg/option.h (GPL-2.0-only)
		"core/cfg/option.h": `
class RendererOption : public Option<RenderType> {
public:
	RendererOption() : Option<RenderType>("pvr.rend",
#if !defined(USE_OPENGL)
			RenderType::Vulkan
#else
			RenderType::OpenGL
#endif
		) {}
};`,
		// flyinghead/flycast v2.7, core/types.h (GPL-2.0-only)
		"core/types.h": `
enum class RenderType {
	OpenGL = 0,
	OpenGL_OIT = 3,
	Vulkan = 4,
};`,
	}
	opts, err := extractFlycast(files)
	if err != nil {
		t.Fatal(err)
	}
	if o := find(t, opts, "config", "rend.WideScreen"); o.Default != false || o.Group != "Rendering" || o.Label != "Wide screen" || o.File != "emu.cfg" {
		t.Errorf("widescreen: %+v", o)
	}
	if o := find(t, opts, "config", "rend.DelayFrameSwapping"); o.Default != true {
		t.Errorf("frame swapping: %+v", o)
	}
	if o := find(t, opts, "config", "rend.PixelBufferSize"); o.Default != float64(512<<20) {
		t.Errorf("buffer: %+v", o)
	}
	if o := find(t, opts, "network", "DNS"); o.Default != "dns.flyca.st" {
		t.Errorf("dns: %+v", o)
	}
	if o := find(t, opts, "config", "pvr.rend"); o.Default != "0" || len(o.Choices) != 3 {
		t.Errorf("renderer: %+v", o)
	}
	find(t, opts, "config", "rend.CrossHairColor1")
	find(t, opts, "config", "Profiler.FrameWarningTime")
	if slices.ContainsFunc(opts, func(o option) bool { return o.Path[0] == "input" }) {
		t.Error("input was read")
	}
}

func TestXemu(t *testing.T) {
	files := map[string]string{
		// xemu-project/xemu v0.8.139, config_spec.yml (GPL-2.0-or-later)
		"config_spec.yml": `general:
  show_welcome:
    type: bool
    default: true
  updates:
    check:
      type: bool
      default: true
  screenshot_dir: string
  skip_boot_anim: bool
input:
  auto_bind:
    type: bool
    default: true
display:
  renderer:
    type: enum
    values: ["NULL", OPENGL, VULKAN]
    default: OPENGL
  quality:
    surface_scale:
      type: integer
      default: 1
net:
  nat:
    forward_ports:
      type: array
      items:
        host: integer
sys:
  mem_limit:
    type: enum
    values: ['64', '128']
    default: '64'
audio:
  volume_limit:
    type: number
    default: 1
`,
	}
	opts, err := extractXemu(files)
	if err != nil {
		t.Fatal(err)
	}
	if o := find(t, opts, "display", "renderer"); o.Default != "OPENGL" || len(o.Choices) != 3 || o.FileType != "string" || o.File != "xemu.toml" {
		t.Errorf("renderer: %+v", o)
	}
	if o := find(t, opts, "display", "quality", "surface_scale"); o.Type != "int" || o.Default != 1.0 {
		t.Errorf("scale: %+v", o)
	}
	if o := find(t, opts, "sys", "mem_limit"); o.Default != "64" || o.FileType != "string" {
		t.Errorf("memory: %+v", o)
	}
	if o := find(t, opts, "general", "skip_boot_anim"); o.Default != false {
		t.Errorf("boot: %+v", o)
	}
	find(t, opts, "general", "updates", "check")
	find(t, opts, "audio", "volume_limit")
	for _, o := range opts {
		if o.Path[0] == "input" || o.Path[0] == "net" {
			t.Errorf("%v was read", o.Path)
		}
	}
	list, _ := settings(opts, nil)
	i := slices.IndexFunc(list, func(s emulators.Setting) bool { return s.Key == "general.updates.check" })
	if i < 0 || !list[i].Fixed || list[i].Default != false {
		t.Errorf("the updater: %+v", list[i])
	}
}

func TestRPCS3(t *testing.T) {
	files := map[string]string{
		// RPCS3/rpcs3 a90588e7 (0.0.43-20582), rpcs3/Emu/system_config.h (GPL-2.0-only)
		"rpcs3/Emu/system_config.h": `
struct cfg_root : cfg::node
{
	struct node_core : cfg::node
	{
		node_core(cfg::node* _this) : cfg::node(_this, "Core") {}

		cfg::_enum<ppu_decoder_type> ppu_decoder{ this, "PPU Decoder", ppu_decoder_type::llvm };
		cfg::_int<1, 8> ppu_threads{ this, "PPU Threads", 2 }; // Amount of PPU threads
		cfg::_bool ppu_debug{ this, "PPU Debug" };
		cfg::uint<0, 100'000> pad_sleep{this, "Pad handler sleep (microseconds)", 1'000, true};
	} core{ this };

	struct node_video : cfg::node
	{
		node_video(cfg::node* _this) : cfg::node(_this, "Video") {}
		cfg::_float<-32, 32> texture_lod_bias{ this, "Texture LOD Bias Addend", 0, true };
		struct node_vk : cfg::node
		{
			node_vk(cfg::node* _this) : cfg::node(_this, "Vulkan") {}
			cfg::string adapter{ this, "Adapter", "", true };
		} vk{ this };
		cfg::_bool after_vk{ this, "After Vulkan", true };
	} video{ this };

	struct node_sys : cfg::node
	{
		node_sys(cfg::node* _this) : cfg::node(_this, "System") {}
		cfg::string system_name{this, "System Name", get_random_system_name()};
	} sys{ this };
	cfg::log_entry log{ this, "Log" };
};`,
		// RPCS3/rpcs3 a90588e7, rpcs3/Emu/system_config_types.h and .cpp (GPL-2.0-only)
		"rpcs3/Emu/system_config_types.h": `
enum class ppu_decoder_type : unsigned char
{
	_static,
	llvm,
};`,
		"rpcs3/Emu/system_config_types.cpp": `
template <>
void fmt_class_string<ppu_decoder_type>::format(std::string& out, u64 arg)
{
	format_enum(out, arg, [](ppu_decoder_type type)
	{
		switch (type)
		{
		case ppu_decoder_type::_static: return "Interpreter (static)";
		case ppu_decoder_type::llvm: return "Recompiler (LLVM)";
		}

		return unknown;
	});
}`,
	}
	opts, err := extractRPCS3(files)
	if err != nil {
		t.Fatal(err)
	}
	if o := find(t, opts, "Core", "PPU Decoder"); o.Default != "Recompiler (LLVM)" || len(o.Choices) != 2 || o.File != "config.yml" {
		t.Errorf("decoder: %+v", o)
	}
	if o := find(t, opts, "Core", "PPU Threads"); o.Default != 2.0 || o.Min != 1 || o.Max != 8 {
		t.Errorf("threads: %+v", o)
	}
	if o := find(t, opts, "Core", "PPU Debug"); o.Default != false {
		t.Errorf("debug: %+v", o)
	}
	if o := find(t, opts, "Core", "Pad handler sleep (microseconds)"); o.Default != 1000.0 || o.Max != 100000 {
		t.Errorf("sleep: %+v", o)
	}
	if o := find(t, opts, "Video", "Texture LOD Bias Addend"); o.Type != "float" || o.Min != -32 {
		t.Errorf("bias: %+v", o)
	}
	find(t, opts, "Video", "Vulkan", "Adapter")
	find(t, opts, "Video", "After Vulkan")
	if slices.ContainsFunc(opts, func(o option) bool { return o.Path[len(o.Path)-1] == "System Name" || o.Path[0] == "Log" }) {
		t.Error("a default made at run time, or the log levels, was read")
	}
}

func TestCemu(t *testing.T) {
	files := map[string]string{
		// cemu-project/Cemu v2.6, src/config/CemuConfig.cpp (MPL-2.0)
		"src/config/CemuConfig.cpp": `
void CemuConfig::Save(XMLConfigParser& parser)
{
	auto config = parser.set("content");
	config.set<bool>("permanent_storage", permanent_storage);
	config.set("mlc_path", mlc_path.GetValue().c_str());
	auto game_path_parser = config.set("GamePaths");
	for (const auto& entry : game_paths)
	{
		game_path_parser.set("Entry", entry.c_str());
	}
	auto graphic = config.set("Graphic");
	graphic.set("api", graphic_api);
	graphic.set("VSync", vsync);
	auto overlay_node = graphic.set("Overlay");
	overlay_node.set("Position", overlay.position);
	auto notification_node = graphic.set("Notification");
	notification_node.set("Position", notification.position);
	auto audio = config.set("Audio");
	audio.set("PadVolume", pad_volume);
	audio.set("TVDevice", boost::nowide::narrow(tv_device).c_str());
#if BOOST_OS_WINDOWS
	audio.set("CrashDumpWindows", crash_dump.GetValue());
#endif
}`,
		// cemu-project/Cemu v2.6, src/config/CemuConfig.h (MPL-2.0)
		"src/config/CemuConfig.h": `
enum GraphicAPI
{
	kOpenGL = 0,
	kVulkan,
};
enum class ScreenPosition
{
	kDisabled = 0,
	kTopLeft,
};
struct CemuConfig
{
	ConfigValue<bool> permanent_storage{ true };
	ConfigValue<std::string> mlc_path{};
	ConfigValue<GraphicAPI> graphic_api{ kVulkan };
	ConfigValue<int> vsync{ 0 }; // 0 = off
	struct
	{
		ScreenPosition position = ScreenPosition::kDisabled;
	} overlay{};
	struct
	{
		ScreenPosition position = ScreenPosition::kTopLeft;
	} notification{};
	sint32 tv_volume = 50, pad_volume = 0, input_volume = 50;
	std::wstring tv_device{ L"default" }, pad_device, input_device;
};`,
	}
	opts, err := extractCemu(files)
	if err != nil {
		t.Fatal(err)
	}
	if o := find(t, opts, "content", "permanent_storage"); o.Default != true || o.File != "settings.xml" {
		t.Errorf("storage: %+v", o)
	}
	if o := find(t, opts, "content", "Graphic", "api"); o.Default != "1" || o.Choices[0].Label != "Open GL" {
		t.Errorf("api: %+v", o)
	}
	if o := find(t, opts, "content", "Graphic", "Overlay", "Position"); o.Default != "0" {
		t.Errorf("overlay: %+v", o)
	}
	if o := find(t, opts, "content", "Graphic", "Notification", "Position"); o.Default != "1" {
		t.Errorf("notification: %+v", o)
	}
	if o := find(t, opts, "content", "Audio", "PadVolume"); o.Default != 0.0 {
		t.Errorf("pad volume: %+v", o)
	}
	if o := find(t, opts, "content", "Audio", "TVDevice"); o.Default != "default" {
		t.Errorf("device: %+v", o)
	}
	find(t, opts, "content", "mlc_path")
	find(t, opts, "content", "Graphic", "VSync")
	if slices.ContainsFunc(opts, func(o option) bool {
		return slices.Contains(o.Path, "Entry") || slices.Contains(o.Path, "CrashDumpWindows")
	}) {
		t.Error("a list entry, or a Windows-only setting, was read")
	}
}

func TestEvalNum(t *testing.T) {
	consts := map[string]string{"A": "4", "B": "A * 2", "Ns::C": "0x10"}
	for expr, want := range map[string]float64{
		"1.0f":                   1,
		"0x01800000U":            0x01800000,
		"(16 / 9.f) / (4 / 3.f)": (16 / 9.0) / (4 / 3.0),
		"(30 * 365 + 7) * 24":    (30*365 + 7) * 24,
		"B + 1":                  9,
		"Ns::C":                  16,
		"static_cast<u32>(1<<6)": 64,
		"(int)A - 1":             3,
		"100'000":                100000,
		"-60*60*24*365*100LL":    -60 * 60 * 24 * 365 * 100,
		"true":                   1,
	} {
		got, ok := evalNum(expr, consts)
		if !ok || got != want {
			t.Errorf("%s: %v %v, want %v", expr, got, ok, want)
		}
	}
	for _, expr := range []string{"Foo()", "Unknown", "1 +", "Ns::D"} {
		if _, ok := evalNum(expr, consts); ok {
			t.Errorf("%s: evaluated", expr)
		}
	}
}

func TestPreprocess(t *testing.T) {
	src := "a\n#ifdef _WIN32\nb\n#elif defined(__linux__)\nc\n#else\nd\n#endif\n#ifndef GUARD_H\ne\n#endif\n#if HOST_CPU == CPU_ARM\nf\n#else\ng\n#endif\n/* x */ h // y\n"
	got := strings.Fields(preprocess(src))
	if !slices.Equal(got, []string{"a", "c", "e", "g", "h"}) {
		t.Errorf("got %v", got)
	}
}
