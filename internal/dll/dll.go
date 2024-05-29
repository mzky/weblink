package dll

import (
	"errors"
	"io"
	"os"

	"github.com/epkgs/mini-blink/internal/log"
	"golang.org/x/sys/windows"
)

const DLL_FILE = "blink.dll"

func Load(releasePath string) (*windows.DLL, error) {

	// 尝试直接加载 DLL
	if loaded, err := windows.LoadDLL(DLL_FILE); err == nil {
		log.Info("直接加载DLL: %s", DLL_FILE)
		return loaded, nil
	}

	// 尝试直接加载释放后的 DLL
	if loaded, err := windows.LoadDLL(releasePath); err == nil {
		log.Info("直接加载DLL: %s", releasePath)
		return loaded, nil
	}

	// 尝试从内嵌资源里打开 DLL 文件
	file, err := FS.Open(DLL_FILE)
	if err != nil {
		return nil, errors.New("无法从默认路径或内嵌资源里找到 blink.dll，err: " + err.Error())
	}
	defer file.Close()

	// 读取内嵌资源 DLL 文件内容
	data, err := io.ReadAll(file)
	if err != nil {
		return nil, errors.New("读取内联DLL出错，err: " + err.Error())
	}

	// 临时文件夹里不存在，则创建
	newFile, err := os.Create(releasePath)
	if err != nil {
		return nil, errors.New("无法创建dll文件，err: " + err.Error())
	}
	defer newFile.Close()

	n, err := newFile.Write(data)
	if err != nil {
		return nil, errors.New("写入dll文件失败，err: " + err.Error())
	}
	if n != len(data) {
		return nil, errors.New("写入校验失败")
	}

	log.Info("从内嵌资源里释放并加载 %s", releasePath)
	return windows.MustLoadDLL(releasePath), nil
}
