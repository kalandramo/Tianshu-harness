// Package rivetpath —— `.rivet` 数据目录路径解析（叶子包）。
//
// 对账 TS `src/config/paths.ts`。
//
// # 为什么单独成包（依赖方向）
//
// 这些函数原先在 `internal/config`。但 `internal/config` 还含 `LoadPermissions`
// （依赖 `internal/agent`），而 `internal/agent → internal/hooks → internal/tools`。
// 于是任何 `internal/tools` 下的代码一旦要读配置文件，就会形成
// `tools → config → agent → hooks → tools` 的**导入环**（第九十七刀 W5 实测撞上）。
//
// 把**纯路径解析**（零项目内依赖，只用 stdlib）下沉为叶子包后，
// 需要路径的包各自 import 本包即可，不再拉进 `config` 的整条依赖链。
package rivetpath

import (
	"os"
	"path/filepath"
	"runtime"
)

// DefaultRivetHome 返回平台默认的 .rivet 数据根（忽略 RIVET_HOME）。
//
// 对账 TS `defaultRivetHome()`（`paths.ts:27-32`）：
//
//	if (process.platform === 'win32') {
//	  return join(process.env.LOCALAPPDATA || join(homedir(), 'AppData', 'Local'), '.rivet')
//	}
//	return join(homedir(), '.rivet')
//
// **Windows 分支是易错点**：TS 在 win32 上用 `%LOCALAPPDATA%\.rivet`，
// 而**不是** `~/.rivet`。若统一用 home，Windows 用户配置永远读不到。
func DefaultRivetHome() string {
	if runtime.GOOS == "windows" {
		if local := os.Getenv("LOCALAPPDATA"); local != "" {
			return filepath.Join(local, ".rivet")
		}
		if home, err := os.UserHomeDir(); err == nil {
			return filepath.Join(home, "AppData", "Local", ".rivet")
		}
		return ".rivet"
	}
	if home, err := os.UserHomeDir(); err == nil {
		return filepath.Join(home, ".rivet")
	}
	return ".rivet"
}

// RivetHome 返回当前生效的数据根（RIVET_HOME 优先，否则平台默认）。
//
// 对账 TS `rivetHome()`：`process.env.RIVET_HOME || defaultRivetHome()`。
func RivetHome() string {
	if home := os.Getenv("RIVET_HOME"); home != "" {
		return home
	}
	return DefaultRivetHome()
}

// UserConfigPath 返回用户全局配置文件的路径。
//
// 对账 TS `userConfigPath()`：`RIVET_CONFIG_PATH` 优先，否则 `<rivetHome>/config.json`。
func UserConfigPath() string {
	if p := os.Getenv("RIVET_CONFIG_PATH"); p != "" {
		return p
	}
	return filepath.Join(RivetHome(), "config.json")
}
