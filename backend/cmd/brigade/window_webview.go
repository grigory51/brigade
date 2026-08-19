//go:build darwin && cgo

package main

/*
#cgo CFLAGS: -x objective-c
#cgo LDFLAGS: -framework Cocoa -framework WebKit
#import <Cocoa/Cocoa.h>
#import <WebKit/WebKit.h>

@interface BrigadeMenuTarget : NSObject
@property(nonatomic, assign) NSWindow *window;
@end

@implementation BrigadeMenuTarget
- (void)openSettings:(id)sender {
	WKWebView *webview = (WKWebView *)[self.window contentView];
	[webview evaluateJavaScript:@"window.location.assign('/settings')" completionHandler:nil];
	[self.window makeKeyAndOrderFront:nil];
}
- (void)goBack:(id)sender {
	WKWebView *webview = (WKWebView *)[self.window contentView];
	if ([webview canGoBack]) [webview goBack];
}
@end

static BrigadeMenuTarget *menuTarget;

// installAppMenu ставит стандартное меню приложения (App + Edit) с системными key-equivalents.
// Без него окно webview не реагирует на cmd+C/V/X/A/Z и cmd+Q: на macOS эти сочетания
// маршрутизируются через пункты ГЛАВНОГО меню (селекторы copy:/paste:/cut:/selectAll:/undo:
// у first responder — им становится сфокусированное поле WebKit), а минимальная обёртка webview
// главное меню не создаёт. Пункты нацелены на nil → AppKit шлёт их по цепочке responder'ов.
static void installAppMenu(void *window) {
	NSApplication *app = [NSApplication sharedApplication];
	NSMenu *menubar = [[NSMenu alloc] init];
	[app setMainMenu:menubar];

	// App-меню: About, Settings (cmd+,), Quit (cmd+Q).
	NSMenuItem *appItem = [[NSMenuItem alloc] init];
	[menubar addItem:appItem];
	NSMenu *appMenu = [[NSMenu alloc] init];
	[appMenu addItemWithTitle:@"About Brigade" action:@selector(orderFrontStandardAboutPanel:) keyEquivalent:@""];
	[appMenu addItem:[NSMenuItem separatorItem]];
	menuTarget = [[BrigadeMenuTarget alloc] init];
	menuTarget.window = (NSWindow *)window;
	NSMenuItem *settings = [appMenu addItemWithTitle:@"Settings…" action:@selector(openSettings:) keyEquivalent:@","];
	[settings setTarget:menuTarget];
	[appMenu addItem:[NSMenuItem separatorItem]];
	[appMenu addItemWithTitle:@"Quit Brigade" action:@selector(terminate:) keyEquivalent:@"q"];
	[appItem setSubmenu:appMenu];

	// Edit-меню: стандартные операции с системными сочетаниями.
	NSMenuItem *editItem = [[NSMenuItem alloc] init];
	[menubar addItem:editItem];
	NSMenu *editMenu = [[NSMenu alloc] initWithTitle:@"Edit"];
	[editMenu addItemWithTitle:@"Undo" action:@selector(undo:) keyEquivalent:@"z"];
	NSMenuItem *redo = [editMenu addItemWithTitle:@"Redo" action:@selector(redo:) keyEquivalent:@"z"];
	[redo setKeyEquivalentModifierMask:(NSEventModifierFlagCommand | NSEventModifierFlagShift)];
	[editMenu addItem:[NSMenuItem separatorItem]];
	[editMenu addItemWithTitle:@"Cut" action:@selector(cut:) keyEquivalent:@"x"];
	[editMenu addItemWithTitle:@"Copy" action:@selector(copy:) keyEquivalent:@"c"];
	[editMenu addItemWithTitle:@"Paste" action:@selector(paste:) keyEquivalent:@"v"];
	[editMenu addItemWithTitle:@"Select All" action:@selector(selectAll:) keyEquivalent:@"a"];
	[editItem setSubmenu:editMenu];

	// Navigate-меню и системный swipe возвращают из внешнего OIDC/passkey flow.
	NSMenuItem *navigateItem = [[NSMenuItem alloc] init];
	[menubar addItem:navigateItem];
	NSMenu *navigateMenu = [[NSMenu alloc] initWithTitle:@"Navigate"];
	NSMenuItem *back = [navigateMenu addItemWithTitle:@"Back" action:@selector(goBack:) keyEquivalent:@"["];
	[back setTarget:menuTarget];
	[navigateItem setSubmenu:navigateMenu];
	WKWebView *webview = (WKWebView *)[(NSWindow *)window contentView];
	[webview setAllowsBackForwardNavigationGestures:YES];
}
*/
import "C"

import (
	"os/exec"

	webview "github.com/webview/webview_go"
)

// showWindow открывает нативное окно (системный WebKit) на url и блокируется до его закрытия.
// Должна вызываться на main-горутине (webview требует main-поток) — так и есть: runDesktop
// исполняется из cobra RunE на главной горутине, сервер вынесен в отдельную горутину.
// Закрытие окна возвращает управление → процесс завершается.
func showWindow(url, title string) {
	w := webview.New(false)
	defer w.Destroy()
	// Меню ставим после webview.New (создаёт NSApplication) и до Run: без него не работают
	// cmd+C/V/X/A/Z и cmd+Q в окне.
	C.installAppMenu(w.Window())
	// Внешние ссылки (другой origin) открываем в системном браузере, а не навигируем окно:
	// у webview нет кнопки «назад», иначе клик по ссылке уводит из приложения без возврата.
	_ = w.Bind("brigadeOpenExternal", func(u string) { _ = exec.Command("open", u).Start() })
	_ = w.Bind("brigadeRevealFile", func(path string) { _ = exec.Command("open", path).Start() })
	w.Init(externalLinksJS)
	w.SetTitle(title)
	w.SetSize(1200, 800, webview.HintNone)
	w.Navigate(url)
	w.Run()
}

// externalLinksJS перехватывает клики по внешним ссылкам и вызовы window.open на внешние URL
// (http/https другого origin, чем приложение) и перенаправляет их в системный браузер через
// bound-функцию brigadeOpenExternal. Внутренняя навигация SPA (тот же origin) не трогается.
// Инжектится на инициализации каждой страницы (webview.Init), слушатель — в capture-фазе.
const externalLinksJS = `(function(){
  function isExternal(u){
    try { var x = new URL(u, location.href);
      return (x.protocol === 'http:' || x.protocol === 'https:') && x.origin !== location.origin;
    } catch(e){ return false; }
  }
  document.addEventListener('click', function(e){
    var a = e.target && e.target.closest ? e.target.closest('a[href]') : null;
    if (a && isExternal(a.href)) {
      e.preventDefault(); e.stopPropagation();
      window.brigadeOpenExternal(a.href);
    }
  }, true);
  var _open = window.open;
  window.open = function(u){
    if (u && isExternal(String(u))) { window.brigadeOpenExternal(String(u)); return null; }
    return _open ? _open.apply(this, arguments) : null;
  };
})();`
