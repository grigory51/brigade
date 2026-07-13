//go:build darwin && cgo

package main

/*
#cgo CFLAGS: -x objective-c
#cgo LDFLAGS: -framework Cocoa
#import <Cocoa/Cocoa.h>

// installAppMenu ставит стандартное меню приложения (App + Edit) с системными key-equivalents.
// Без него окно webview не реагирует на cmd+C/V/X/A/Z и cmd+Q: на macOS эти сочетания
// маршрутизируются через пункты ГЛАВНОГО меню (селекторы copy:/paste:/cut:/selectAll:/undo:
// у first responder — им становится сфокусированное поле WebKit), а минимальная обёртка webview
// главное меню не создаёт. Пункты нацелены на nil → AppKit шлёт их по цепочке responder'ов.
static void installAppMenu(void) {
	NSApplication *app = [NSApplication sharedApplication];
	NSMenu *menubar = [[NSMenu alloc] init];
	[app setMainMenu:menubar];

	// App-меню (первый пункт панели): Quit (cmd+Q).
	NSMenuItem *appItem = [[NSMenuItem alloc] init];
	[menubar addItem:appItem];
	NSMenu *appMenu = [[NSMenu alloc] init];
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
}
*/
import "C"

import webview "github.com/webview/webview_go"

// showWindow открывает нативное окно (системный WebKit) на url и блокируется до его закрытия.
// Должна вызываться на main-горутине (webview требует main-поток) — так и есть: runDesktop
// исполняется из cobra RunE на главной горутине, сервер вынесен в отдельную горутину.
// Закрытие окна возвращает управление → процесс завершается.
func showWindow(url, title string) {
	w := webview.New(false)
	defer w.Destroy()
	// Меню ставим после webview.New (создаёт NSApplication) и до Run: без него не работают
	// cmd+C/V/X/A/Z и cmd+Q в окне.
	C.installAppMenu()
	w.SetTitle(title)
	w.SetSize(1200, 800, webview.HintNone)
	w.Navigate(url)
	w.Run()
}
