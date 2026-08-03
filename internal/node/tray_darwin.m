//go:build darwin && cgo

#import <AppKit/AppKit.h>

extern void yuanshuTrayAction(int action);

enum {
    YuanshuTrayOpen = 1,
    YuanshuTrayPair = 2,
    YuanshuTrayReload = 3,
    YuanshuTrayCopyDiagnostics = 4,
    YuanshuTrayAutostart = 5,
    YuanshuTrayReviewConfig = 6,
    YuanshuTrayQuit = 7,
};

@interface YuanshuTrayDelegate : NSObject <NSApplicationDelegate, NSMenuDelegate>
@property(nonatomic, strong) NSStatusItem *statusItem;
@property(nonatomic, strong) NSMenuItem *stateItem;
@property(nonatomic, strong) NSMenuItem *autostartItem;
@property(nonatomic, copy) NSString *state;
@property(nonatomic) BOOL autostartEnabled;
@end

@implementation YuanshuTrayDelegate

- (void)applicationDidFinishLaunching:(NSNotification *)notification {
    (void)notification;
    self.statusItem = [[NSStatusBar systemStatusBar] statusItemWithLength:NSSquareStatusItemLength];
    NSStatusBarButton *button = self.statusItem.button;
    if (@available(macOS 11.0, *)) {
        NSImage *image = [NSImage imageWithSystemSymbolName:@"point.3.connected.trianglepath.dotted"
                                  accessibilityDescription:@"Yuanshu Node"];
        image.template = YES;
        button.image = image;
    } else {
        button.title = @"枢";
    }
    button.toolTip = @"Yuanshu Node";

    NSMenu *menu = [[NSMenu alloc] initWithTitle:@"Yuanshu Node"];
    menu.delegate = self;
    self.stateItem = [[NSMenuItem alloc] initWithTitle:@"Yuanshu Node · Starting" action:nil keyEquivalent:@""];
    self.stateItem.enabled = NO;
    [menu addItem:self.stateItem];
    [menu addItem:[NSMenuItem separatorItem]];
    [menu addItem:[self actionItem:@"Open Node Control Center" action:YuanshuTrayOpen key:@","]];
    [menu addItem:[self actionItem:@"Pair a Browser" action:YuanshuTrayPair key:@""]];
    [menu addItem:[self actionItem:@"Reload and Reconnect" action:YuanshuTrayReload key:@""]];
    [menu addItem:[self actionItem:@"Copy Diagnostics" action:YuanshuTrayCopyDiagnostics key:@""]];
    [menu addItem:[NSMenuItem separatorItem]];
    self.autostartItem = [self actionItem:@"Start at Login" action:YuanshuTrayAutostart key:@""];
    [menu addItem:self.autostartItem];
    [menu addItem:[self actionItem:@"Review Pending Changes…" action:YuanshuTrayReviewConfig key:@""]];
    [menu addItem:[NSMenuItem separatorItem]];
    [menu addItem:[self actionItem:@"Quit Yuanshu Node" action:YuanshuTrayQuit key:@"q"]];
    self.statusItem.menu = menu;
    [self refresh];
}

- (NSMenuItem *)actionItem:(NSString *)title action:(NSInteger)action key:(NSString *)key {
    NSMenuItem *item = [[NSMenuItem alloc] initWithTitle:title action:@selector(performAction:) keyEquivalent:key];
    item.target = self;
    item.tag = action;
    return item;
}

- (void)performAction:(NSMenuItem *)sender {
    yuanshuTrayAction((int)sender.tag);
}

- (void)menuWillOpen:(NSMenu *)menu {
    (void)menu;
    [self refresh];
}

- (void)refresh {
    NSString *state = self.state.length > 0 ? self.state : @"Starting";
    self.stateItem.title = [NSString stringWithFormat:@"Yuanshu Node · %@", state];
    self.statusItem.button.toolTip = self.stateItem.title;
    self.autostartItem.state = self.autostartEnabled ? NSControlStateValueOn : NSControlStateValueOff;
}

@end

static YuanshuTrayDelegate *YuanshuDelegate;

void YuanshuTrayRun(void) {
    @autoreleasepool {
        NSApplication *application = [NSApplication sharedApplication];
        [application setActivationPolicy:NSApplicationActivationPolicyAccessory];
        YuanshuDelegate = [[YuanshuTrayDelegate alloc] init];
        application.delegate = YuanshuDelegate;
        [application run];
        if (YuanshuDelegate.statusItem != nil) {
            [[NSStatusBar systemStatusBar] removeStatusItem:YuanshuDelegate.statusItem];
        }
        application.delegate = nil;
        YuanshuDelegate = nil;
    }
}

void YuanshuTrayStop(void) {
    dispatch_async(dispatch_get_main_queue(), ^{
        [NSApp stop:nil];
        NSEvent *wake = [NSEvent otherEventWithType:NSEventTypeApplicationDefined
                                           location:NSZeroPoint
                                      modifierFlags:0
                                          timestamp:0
                                       windowNumber:0
                                            context:nil
                                            subtype:0
                                              data1:0
                                              data2:0];
        [NSApp postEvent:wake atStart:NO];
    });
}

void YuanshuTrayUpdate(const char *state, int autostartEnabled) {
    NSString *value = state == NULL ? @"Starting" : [NSString stringWithUTF8String:state];
    dispatch_async(dispatch_get_main_queue(), ^{
        YuanshuDelegate.state = value;
        YuanshuDelegate.autostartEnabled = autostartEnabled != 0;
        [YuanshuDelegate refresh];
    });
}

void YuanshuTrayOpenURL(const char *target) {
    if (target == NULL) return;
    NSString *value = [NSString stringWithUTF8String:target];
    dispatch_async(dispatch_get_main_queue(), ^{
        NSURL *url = [NSURL URLWithString:value];
        if (url != nil) [[NSWorkspace sharedWorkspace] openURL:url];
    });
}

void YuanshuTrayCopy(const char *value) {
    if (value == NULL) return;
    NSString *text = [NSString stringWithUTF8String:value];
    dispatch_async(dispatch_get_main_queue(), ^{
        NSPasteboard *pasteboard = [NSPasteboard generalPasteboard];
        [pasteboard clearContents];
        [pasteboard setString:text forType:NSPasteboardTypeString];
    });
}

void YuanshuTrayShowError(const char *message) {
    NSString *text = message == NULL ? @"The requested action failed." : [NSString stringWithUTF8String:message];
    dispatch_async(dispatch_get_main_queue(), ^{
        NSAlert *alert = [[NSAlert alloc] init];
        alert.messageText = @"Yuanshu Node";
        alert.informativeText = text;
        alert.alertStyle = NSAlertStyleWarning;
        [alert addButtonWithTitle:@"OK"];
        [alert runModal];
    });
}

int YuanshuTrayConfirmConfig(const char *identifier, const char *fields) {
    __block NSInteger result = 0;
    NSString *changeID = identifier == NULL ? @"Unknown" : [NSString stringWithUTF8String:identifier];
    NSString *fieldList = fields == NULL ? @"Protected settings" : [NSString stringWithUTF8String:fields];
    dispatch_sync(dispatch_get_main_queue(), ^{
        NSAlert *alert = [[NSAlert alloc] init];
        alert.messageText = @"Review protected Node settings";
        alert.informativeText = [NSString stringWithFormat:@"Change: %@\nFields: %@\n\nOnly approve this change if you initiated it.", changeID, fieldList];
        alert.alertStyle = NSAlertStyleWarning;
        [alert addButtonWithTitle:@"Approve"];
        [alert addButtonWithTitle:@"Reject"];
        [alert addButtonWithTitle:@"Cancel"];
        NSModalResponse response = [alert runModal];
        if (response == NSAlertFirstButtonReturn) result = 1;
        else if (response == NSAlertSecondButtonReturn) result = 2;
    });
    return (int)result;
}
