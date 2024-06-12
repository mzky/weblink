package blink

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"unsafe"

	"github.com/epkgs/mini-blink/internal/log"
	"github.com/epkgs/mini-blink/internal/utils"
)

type OnConsoleCallback func(level int, message, sourceName string, sourceLine int, stackTrace string)
type OnClosingCallback func() bool // 返回 false 拒绝关闭窗口
type OnDestroyCallback func()
type OnLoadUrlBeginCallback func(url string, job WkeNetJob) bool
type OnLoadUrlEndCallback func(url string, job WkeNetJob, buf []byte)
type OnDocumentReadyCallback func(frame WkeWebFrameHandle)
type OnDidCreateScriptContextCallback func(frame WkeWebFrameHandle, context uintptr, exGroup, worldId int)
type OnTitleChangedCallback func(title string)
type OnDownloadCallback func(url string)

type View struct {
	Hwnd     WkeHandle
	Window   *Window
	DevTools *View

	mb     *Blink
	parent *View

	eventCallbacks map[string]func()

	onClosingCallbacks       map[string]OnClosingCallback
	onDestroyCallbacks       map[string]OnDestroyCallback
	onLoadUrlBeginCallbacks  map[string]OnLoadUrlBeginCallback
	onLoadUrlEndCallbacks    map[string]OnLoadUrlEndCallback
	onDocumentReadyCallbacks map[string]OnDocumentReadyCallback
	onTitleChangedCallbacks  map[string]OnTitleChangedCallback
	onDownloadCallbacks      map[string]OnDownloadCallback

	onDidCreateScriptContextCallbacks map[string]OnDidCreateScriptContextCallback
}

func NewView(mb *Blink, hwnd WkeHandle, windowType WkeWindowType, parent ...*View) *View {

	var p *View = nil

	if len(parent) >= 1 {
		p = parent[0]
	}

	view := &View{
		mb:     mb,
		Hwnd:   hwnd,
		parent: p,

		eventCallbacks: make(map[string]func()),

		onClosingCallbacks:       make(map[string]OnClosingCallback),
		onDestroyCallbacks:       make(map[string]OnDestroyCallback),
		onLoadUrlBeginCallbacks:  make(map[string]OnLoadUrlBeginCallback),
		onLoadUrlEndCallbacks:    make(map[string]OnLoadUrlEndCallback),
		onDocumentReadyCallbacks: make(map[string]OnDocumentReadyCallback),
		onTitleChangedCallbacks:  make(map[string]OnTitleChangedCallback),
		onDownloadCallbacks:      make(map[string]OnDownloadCallback),

		onDidCreateScriptContextCallbacks: make(map[string]OnDidCreateScriptContextCallback),
	}

	view.Window = newWindow(mb, view, windowType)

	view.SetLocalStorageFullPath(view.mb.Config.GetStoragePath())
	view.SetCookieJarFullPath(view.mb.Config.GetCookieFileABS())

	view.registerFileSystem()

	view.registerOnClosing()
	view.registerOnDestroy()
	view.registerOnLoadUrlBegin()
	view.registerOnLoadUrlEnd()
	view.registerOnDocumentReady()
	view.registerOnTitleChanged()
	view.registerOnDownload()
	view.registerOnDidCreateScriptContext()

	view.listenMinBtnClick()
	view.listenMaxBtnClick()
	view.listenCloseBtnClick()
	view.listenCaptionDrag()

	view.addToPool()
	view.injectBootScripts()

	// 添加默认下载操作
	view.OnDownload(func(url string) {
		view.mb.Downloader.Download(url)
	})

	return view
}

func (v *View) addToPool() {

	locker.Lock()
	defer locker.Unlock()

	v.mb.views[v.Hwnd] = v
	v.mb.windows[v.Window.Hwnd] = v.Window

	log.Debug("Add view to BLINK, now SIZE: %d", len(v.mb.views))

	v.OnDestroy(func() {

		func() {
			locker.Lock()
			defer locker.Unlock()

			delete(v.mb.windows, v.Window.Hwnd)
			delete(v.mb.views, v.Hwnd)

		}()

		for _, child := range v.mb.views {
			if child.parent == v {
				child.DestroyWindow()
			}
		}

	})
}

func (v *View) injectBootScripts() {
	var script string

	for _, s := range v.mb.bootScripts {
		script += s + ";\n"
	}

	v.OnDidCreateScriptContext(func(frame WkeWebFrameHandle, context uintptr, exGroup, worldId int) {
		v.mb.CallFunc("wkeRunJS", uintptr(v.Hwnd), StringToPtr(script))
	})
}

func (v *View) ShowWindow() {
	v.Window.Show()
}

func (v *View) HideWindow() {
	v.Window.Hide()
}

func (v *View) CloseWindow() {
	v.Window.Close()
}

func (v *View) DestroyWindow() {
	v.Window.Destroy()
}

func (v *View) Reload() bool {
	r, _, _ := v.mb.CallFunc("wkeReload", uintptr(v.Hwnd))
	return r != 0
}

func (v *View) ForceReload() {
	v.LoadURL(v.GetURL())
}

func (v *View) LoadURL(url string) {
	v.mb.CallFunc("wkeLoadURL", uintptr(v.Hwnd), StringToPtr(url))
}

func (v *View) GetURL() string {
	r, _, _ := v.mb.CallFunc("wkeGetURL", uintptr(v.Hwnd))
	return PtrToString(r)
}

// 设置local storage的全路径。如“c:\mb\LocalStorage\”
// 注意：这个接口只能接受目录。
func (v *View) SetLocalStorageFullPath(path string) {
	v.mb.CallFunc("wkeSetLocalStorageFullPath", uintptr(v.Hwnd), StringToWCharPtr(path))
}

// 设置cookie的全路径+文件名，如“c:\mb\cookie.dat”
func (v *View) SetCookieJarFullPath(path string) {
	v.mb.CallFunc("wkeSetCookieJarFullPath", uintptr(v.Hwnd), StringToWCharPtr(path))
}

func (v *View) GetWindowHandle() WkeHandle {
	ptr, _, _ := v.mb.CallFunc("wkeGetWindowHandle", uintptr(v.Hwnd))
	return WkeHandle(ptr)
}

func (v *View) Resize(width, height int32) {
	v.mb.CallFunc("wkeResize", uintptr(v.Hwnd), uintptr(width), uintptr(height))
}

func (v *View) registerFileSystem() {
	v.OnLoadUrlBegin(func(url string, job WkeNetJob) bool {

		f := v.mb.Resource.GetFile(url)

		// 找不到文件
		if f == nil {
			return false
		}

		defer f.Close()

		byt, err := io.ReadAll(f)
		// 读取文件错误
		if err != nil {
			return false
		}

		v.mb.NetSetData(job, byt)

		// 找到并读取正常，返回 true 取消后继的网络请求
		return true

	})
}

// 可以添加多个 callback，将按照加入顺序依次执行
//
// callback 返回 false 拒绝关闭窗口
func (v *View) OnClosing(callback OnClosingCallback) (stop func()) {

	key := utils.RandString(10)

	v.onClosingCallbacks[key] = callback

	return func() {
		delete(v.onClosingCallbacks, key)
	}
}
func (v *View) registerOnClosing() {
	var handler WkeWindowClosingCallback = func(view WkeHandle, param uintptr) (boolRes uintptr) {
		log.Debug("Trigger view.OnClosing")
		for _, callback := range v.onClosingCallbacks {
			if ok := callback(); !ok {
				return BoolToPtr(false)
			}
		}
		return BoolToPtr(true)
	}
	v.mb.CallFunc("wkeOnWindowClosing", uintptr(v.Hwnd), CallbackToPtr(handler), 0)
}

// 可以添加多个 callback，将按照加入顺序依次执行
func (v *View) OnDestroy(callback OnDestroyCallback) (stop func()) {

	key := utils.RandString(10)

	v.onDestroyCallbacks[key] = callback

	return func() {
		delete(v.onDestroyCallbacks, key)
	}
}
func (v *View) registerOnDestroy() {
	var handler WkeWindowDestroyCallback = func(view WkeHandle, param uintptr) (voidRes uintptr) {
		log.Debug("Trigger view.OnDestroy")
		for _, callback := range v.onDestroyCallbacks {
			callback()
		}
		return
	}
	v.mb.CallFunc("wkeOnWindowDestroy", uintptr(v.Hwnd), CallbackToPtr(handler), 0)
}

func (v *View) OnLoadUrlBegin(callback OnLoadUrlBeginCallback) (stop func()) {

	key := utils.RandString(10)

	v.onLoadUrlBeginCallbacks[key] = callback

	return func() {
		delete(v.onLoadUrlBeginCallbacks, key)
	}
}
func (v *View) registerOnLoadUrlBegin() {
	var handler = func(view, param, url, job uintptr) (boolPtr uintptr) {
		for _, callback := range v.onLoadUrlBeginCallbacks {
			// 返回 true 则中断、阻止后面的网络请求
			if callback(PtrToString(url), WkeNetJob(job)) {
				return 1 // 返回 true 的 uintptr
			}
		}
		return 0 // 返回 false 的 uintptr
	}

	v.mb.CallFunc("wkeOnLoadUrlBegin", uintptr(v.Hwnd), CallbackToPtr(handler), 0)
}

func (v *View) OnLoadUrlEnd(callback OnLoadUrlEndCallback) (stop func()) {

	key := utils.RandString(10)

	v.onLoadUrlEndCallbacks[key] = callback

	return func() {
		delete(v.onLoadUrlEndCallbacks, key)
	}
}
func (v *View) registerOnLoadUrlEnd() {
	var handler = func(view, param, url, job, buf, len uintptr) uintptr {

		_url := PtrToString(url)
		_job := WkeNetJob(job)
		_buf := CopyBytes(buf, int(len))
		for _, callback := range v.onLoadUrlEndCallbacks {
			callback(_url, _job, _buf)
		}
		return 0
	}
	v.mb.CallFunc("wkeOnLoadUrlEnd", uintptr(v.Hwnd), CallbackToPtr(handler), 0)
}

func (v *View) OnDocumentReady(callback OnDocumentReadyCallback) (stop func()) {

	key := utils.RandString(10)

	v.onDocumentReadyCallbacks[key] = callback

	return func() {
		delete(v.onDocumentReadyCallbacks, key)
	}
}
func (v *View) registerOnDocumentReady() {
	var cb WkeDocumentReady2Callback = func(view WkeHandle, param uintptr, frame WkeWebFrameHandle) (voidRes uintptr) {

		for _, callback := range v.onDocumentReadyCallbacks {
			callback(frame)
		}

		return 0
	}
	v.mb.CallFunc("wkeOnDocumentReady2", uintptr(v.Hwnd), CallbackToPtr(cb), 0)
}

func (v *View) IsMainFrame(frameId WkeWebFrameHandle) bool {
	p, _, _ := v.mb.CallFunc("wkeIsMainFrame", uintptr(v.Hwnd), uintptr(frameId))

	return p != 0
}

func (v *View) GetRect() *WkeRect {
	ptr, _, _ := v.mb.CallFunc("wkeGetCaretRect2", uintptr(v.Hwnd))
	return (*WkeRect)(unsafe.Pointer(ptr))
}

// 仅作用于 主frame，会自动判断是否 document ready
func (v *View) RunJS(script string) {

	if v.IsDocumentReady() {
		v.mb.CallFunc("wkeRunJS", uintptr(v.Hwnd), StringToPtr(script))
		return
	}

	var stop func()
	stop = v.OnDocumentReady(func(frame WkeWebFrameHandle) {
		if !v.IsMainFrame(frame) {
			return
		}
		v.mb.CallFunc("wkeRunJS", uintptr(v.Hwnd), StringToPtr(script))

		if stop != nil {
			stop() // 执行完毕就停止，不重复执行
		}
	})
}

// 可指定 frame，会自动判断是否 document ready
func (v *View) RunJsByFrame(frame WkeWebFrameHandle, script string, isInClosure bool) {

	if v.IsDocumentReady() {
		v.mb.CallFunc("wkeRunJsByFrame", uintptr(frame), StringToPtr(script), BoolToPtr(isInClosure))
		return
	}

	var stop func()
	stop = v.OnDocumentReady(func(readyFrame WkeWebFrameHandle) {
		if readyFrame != frame {
			return
		}
		v.mb.CallFunc("wkeRunJsByFrame", uintptr(frame), StringToPtr(script), BoolToPtr(isInClosure))

		if stop != nil {
			stop() // 执行完毕就停止，不重复执行
		}
	})
}

func (v *View) RunJsFunc(funcName string, args ...interface{}) (result chan interface{}) {

	return v.mb.IPC.RunJSFunc(v, funcName, args...)
}

func (v *View) OnDidCreateScriptContext(callback OnDidCreateScriptContextCallback) (stop func()) {

	key := utils.RandString(8)
	v.onDidCreateScriptContextCallbacks[key] = callback

	return func() {
		delete(v.onDidCreateScriptContextCallbacks, key)
	}
}
func (v *View) registerOnDidCreateScriptContext() {

	var cb WkeDidCreateScriptContextCallback = func(view WkeHandle, param uintptr, frame WkeWebFrameHandle, context uintptr, exGroup, worldId int) (voidRes uintptr) {

		for _, callback := range v.onDidCreateScriptContextCallbacks {
			callback(frame, context, exGroup, worldId)
		}
		return 0
	}
	v.mb.CallFunc("wkeOnDidCreateScriptContext", uintptr(v.Hwnd), CallbackToPtr(cb), 0)
}

// JS.bind(".mb-minimize-btn", "click", func)
func (v *View) AddEventListener(selector, eventType string, callback func(), preScripts ...string) {

	script := `
	(()=>{
		const VIEW_HANDLE = '%s';
		const JS_IPC = '%s';
		const selector = '%s';
		const eventType = '%s';
		
		const els = document.querySelectorAll(selector);
		
		const handler = function(e) {
			%s; // pre-event
		
			e.preventDefault();
		
			const ipc = window.top[JS_IPC]
			ipc.sent('addEventListener', VIEW_HANDLE, selector, eventType)
		};
		
		for (let i = 0; i < els.length; i++) {
			els[i].removeEventListener(eventType, handler);
			els[i].addEventListener(eventType, handler);
		}
	
	})();
	`

	script = fmt.Sprintf(
		script,
		strconv.FormatUint(uint64(v.Hwnd), 10),
		JS_IPC,
		selector,
		eventType,
		strings.Join(preScripts, ";"),
	)

	if !v.mb.IPC.HasChannel("addEventListener") {

		v.mb.IPC.Handle("addEventListener", func(hwndStr, selector, eventType string) {
			hwnd, err := strconv.Atoi(hwndStr)
			if err != nil {
				log.Error("hwnd 转换失败：%s", err.Error())
				return
			}

			view, exist := v.mb.GetViewByHandle(WkeHandle(hwnd))
			if !exist {
				return
			}

			key := selector + " " + eventType

			callback, exist := view.eventCallbacks[key]
			if !exist {
				return
			}

			callback()
		})

	}

	key := selector + " " + eventType

	v.eventCallbacks[key] = callback // 增加 callback

	v.RunJS(script)
}

func (v *View) RemoveEventListener(selector, eventType string) {

	key := selector + " " + eventType

	delete(v.eventCallbacks, key)
}

func (v *View) listenMinBtnClick() {
	v.AddEventListener(".mb-btn-min", "click", func() {
		v.Window.Minimize()
	})

}

func (v *View) listenMaxBtnClick() {

	preScript := `this.classList.toggle('maximized');`

	v.AddEventListener(".mb-btn-max", "click", func() {
		if v.Window.IsMaximized() {
			v.Window.Restore()
		} else {
			v.Window.Maximize()
		}
	}, preScript)
}

func (v *View) listenCloseBtnClick() {
	v.AddEventListener(".mb-btn-close", "click", func() {
		v.CloseWindow()
	})
}

// 监听窗口拖动
func (v *View) listenCaptionDrag() {

	preScript := `if(e.target.closest('.mb-caption-nodrag')) return;`

	v.AddEventListener(".mb-caption-drag", "mousedown", func() {
		if v.Window.IsMaximized() {
			return
		}
		v.Window.EnableDragging()
	}, preScript)
}

func (v *View) OnConsole(callback OnConsoleCallback) {

	var cb WkeConsoleCallback = func(view WkeHandle, param uintptr, level WkeConsoleLevel, message, sourceName WkeString, sourceLine uint32, stackTrace WkeString) (voidRes uintptr) {

		callback(int(level), v.mb.GetString(message), v.mb.GetString(sourceName), int(sourceLine), v.mb.GetString(stackTrace))

		return 0
	}

	v.mb.CallFunc("wkeOnConsole", uintptr(v.Hwnd), CallbackToPtr(cb), 0)
}

func (v *View) IsDocumentReady() bool {
	p, _, _ := v.mb.CallFunc("wkeIsDocumentReady", uintptr(v.Hwnd))
	return p != 0
}

func (v *View) OnTitleChanged(callback OnTitleChangedCallback) (stop func()) {

	key := utils.RandString(10)

	v.onTitleChangedCallbacks[key] = callback

	return func() {
		delete(v.onTitleChangedCallbacks, key)
	}
}
func (v *View) registerOnTitleChanged() {

	var cb WkeTitleChangedCallback = func(view WkeHandle, param uintptr, title WkeString) (voidRes uintptr) {
		_title := v.mb.GetString(title)

		for _, callback := range v.onTitleChangedCallbacks {
			callback(_title)
		}
		return
	}

	v.mb.CallFunc("wkeOnTitleChanged", uintptr(v.Hwnd), CallbackToPtr(cb), 0)
}

func (v *View) OnDownload(callback OnDownloadCallback) (stop func()) {

	key := utils.RandString(10)

	v.onDownloadCallbacks[key] = callback

	return func() {
		delete(v.onDownloadCallbacks, key)
	}
}
func (v *View) registerOnDownload() {
	var cb WkeDownloadCallback = func(view WkeHandle, param uintptr, url uintptr) (voidRes uintptr) {
		link := PtrToString(url)
		for _, callback := range v.onDownloadCallbacks {
			callback(link)
		}
		return
	}

	v.mb.CallFunc("wkeOnDownload", uintptr(v.Hwnd), CallbackToPtr(cb), 0)
}

func (v *View) GetMainWebFrame() (WkeWebFrameHandle, error) {
	r1, _, err := v.mb.CallFunc("wkeWebFrameGetMainFrame", uintptr(v.Hwnd))
	if err != nil {
		return 0, err
	}

	return WkeWebFrameHandle(r1), nil
}

type WithWkePrintSettings func(setting *WkePrintSettings)

func (v *View) SaveToPDF(path string, withSetting ...WithWkePrintSettings) error {
	frameId, err := v.GetMainWebFrame()
	if err != nil {
		return err
	}

	return v.SaveWebFrameToPDF(frameId, path, withSetting...)
}

func (v *View) SaveWebFrameToPDF(frameId WkeWebFrameHandle, path string, withSetting ...WithWkePrintSettings) error {

	// 假设A4纸张，每边1厘米的边距，DPI为600
	setting := WkePrintSettings{
		structSize:               48, // 结构体大小，每个 int 为4, 12个int为48（极个别 C 编译器的int大小为8，暂不予考虑）
		Dpi:                      600,
		Width:                    4960, // A4纸张宽度转换为像素（600 DPI）
		Height:                   7016, // A4纸张高度转换为像素（600 DPI）
		MarginTop:                236,  // 1厘米边距转换为像素（600 DPI）
		MarginBottom:             236,
		MarginLeft:               236,
		MarginRight:              236,
		IsPrintPageHeadAndFooter: TRUE,  // 是否打印页眉页脚
		IsPrintBackgroud:         TRUE,  // 是否打印背景
		IsLandscape:              FALSE, // 是否横向打印
		IsPrintToMultiPage:       FALSE, // 是否打印到多页
	}

	for _, withSet := range withSetting {
		withSet(&setting)
	}

	r1, _, err := v.mb.CallFunc("wkeUtilPrintToPdf", uintptr(v.Hwnd), uintptr(frameId), uintptr(unsafe.Pointer(&setting)))
	if err != nil {
		return err
	}
	// 释放内存
	defer v.mb.CallFuncAsync("wkeUtilRelasePrintPdfDatas", r1)

	pd := (*wkePdfDatas)(unsafe.Pointer(r1))

	if pd.count == 0 {
		return errors.New("生成 PDF 失败")
	}

	sizes := SlicesFromPtr[uintptr](pd.sizes, pd.count)
	datasPtrs := SlicesFromPtr[uintptr](pd.datas, pd.count)
	// sizes := (*(*[1 << 31]uintptr)(unsafe.Pointer(pd.sizes)))[:pd.count:pd.count]
	// datasPtrs := (*(*[1 << 31]uintptr)(unsafe.Pointer(pd.datas)))[:pd.count:pd.count]

	if pd.count == 1 {
		dataPtr := datasPtrs[0]
		size := sizes[0]

		chunk := SlicesFromPtr[byte](dataPtr, int(size))
		// chunk := (*(*[1 << 31]byte)(unsafe.Pointer(dataPtr)))[:size:size]
		return writeFile(path, chunk)
	}

	// 遍历slice里的二进制数据
	for i := 0; i < pd.count; i++ {
		dataPtr := datasPtrs[i]
		size := sizes[i]

		chunk := SlicesFromPtr[byte](dataPtr, int(size))
		// chunk := (*(*[1 << 31]byte)(unsafe.Pointer(dataPtr)))[:size:size]

		// 将数据写入文件
		if err := writeFile(getFilePathWithIndex(path, i+1), chunk); err != nil {
			return err
		}
	}

	return nil
}

func writeFile(path string, data []byte) error {

	// 使用 os.Create 创建文件，如有内容则会截断

	file, err := os.Create(getUnusedPath(path))
	if err != nil {
		return err
	}
	defer file.Close() // 确保在函数返回前关闭文件

	_, err = file.Write(data)

	return err
}

func getFilePathWithIndex(originalPath string, index int) string {
	base := filepath.Base(originalPath)
	dir := filepath.Dir(originalPath)
	ext := filepath.Ext(base)
	baseWithoutExt := base[:len(base)-len(ext)]

	return filepath.Join(dir, fmt.Sprintf("%s-%d%s", baseWithoutExt, index, ext))
}

// getUnusedPath 检查文件是否存在，并返回一个唯一的文件路径
func getUnusedPath(originalPath string) string {

	// 检查文件是否存在
	if _, err := os.Stat(originalPath); os.IsNotExist(err) {
		// 文件不存在，返回新路径
		return originalPath
	}

	base := filepath.Base(originalPath)
	dir := filepath.Dir(originalPath)
	ext := filepath.Ext(base)
	baseWithoutExt := base[:len(base)-len(ext)]

	index := 1
	for {
		// 构造新的文件名
		newBase := fmt.Sprintf("%s(%d)%s", baseWithoutExt, index, ext)
		newPath := filepath.Join(dir, newBase)

		// 检查文件是否存在
		if _, err := os.Stat(newPath); os.IsNotExist(err) {
			// 文件不存在，返回新路径
			return newPath
		}

		// 文件存在，增加索引并重试
		index++
	}
}
