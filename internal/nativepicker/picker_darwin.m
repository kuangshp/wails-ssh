//go:build darwin && cgo

#import <Cocoa/Cocoa.h>
#include <string.h>
#include "picker_darwin.h"

// All request state is owned by Cocoa and accessed only on its main thread.
// No Go pointers or callbacks cross the C boundary.
@interface SSHUploadPickerRequest : NSObject
@property(nonatomic, strong) NSOpenPanel *panel;
@property(nonatomic, strong) id closeObserver;
@property(nonatomic, strong) id terminateObserver;
@property(nonatomic, copy) void (^completion)(NSArray<NSString *> *, NSString *);
@property(nonatomic) BOOL finished;
- (void)finishWithPaths:(NSArray<NSString *> *)paths error:(NSString *)error;
- (void)cancel;
@end

@implementation SSHUploadPickerRequest
- (void)finishWithPaths:(NSArray<NSString *> *)paths error:(NSString *)error {
    if (self.finished) return;
    self.finished = YES;
    NSNotificationCenter *center = NSNotificationCenter.defaultCenter;
    if (self.closeObserver) [center removeObserver:self.closeObserver];
    if (self.terminateObserver) [center removeObserver:self.terminateObserver];
    self.closeObserver = nil;
    self.terminateObserver = nil;
    void (^completion)(NSArray<NSString *> *, NSString *) = self.completion;
    self.completion = nil;
    self.panel = nil;
    if (completion) completion(paths ?: @[], error);
}

- (void)cancel {
    NSOpenPanel *panel = self.panel;
    [panel cancel:nil];
    // Window teardown may not deliver the panel's completion immediately.
    // The finished guard also handles a later completion without signalling twice.
    [self finishWithPaths:@[] error:nil];
}
@end

static NSWindow *SSHUploadPickerParent(void) {
    NSWindow *window = NSApp.keyWindow;
    if (window.sheetParent) window = window.sheetParent;
    if (!window || [window isKindOfClass:NSPanel.class]) window = NSApp.mainWindow;
    if (!window || [window isKindOfClass:NSPanel.class]) return nil;
    return window;
}

SSHUploadPickerResult ssh_pick_upload_paths(void) {
    if (NSThread.isMainThread) {
        return (SSHUploadPickerResult){NULL, strdup("文件选择器不能在界面主线程同步调用")};
    }

    __block SSHUploadPickerResult result = {NULL, NULL};
    dispatch_semaphore_t done = dispatch_semaphore_create(0);
    dispatch_async(dispatch_get_main_queue(), ^{
        @autoreleasepool {
            SSHUploadPickerRequest *request = [SSHUploadPickerRequest new];
            request.completion = ^(NSArray<NSString *> *paths, NSString *error) {
                if (error) {
                    result.error_message = strdup(error.UTF8String);
                } else {
                    NSError *serializationError = nil;
                    NSData *data = [NSJSONSerialization dataWithJSONObject:paths options:0 error:&serializationError];
                    if (data) {
                        NSString *json = [[NSString alloc] initWithData:data encoding:NSUTF8StringEncoding];
                        result.paths_json = strdup(json.UTF8String);
                    } else {
                        result.error_message = strdup("无法读取文件选择结果");
                    }
                }
                dispatch_semaphore_signal(done);
            };

            NSWindow *parent = SSHUploadPickerParent();
            if (!parent || !NSApp.running) {
                [request finishWithPaths:nil error:@"应用窗口尚未就绪，请稍后重试"];
                return;
            }
            if (parent.attachedSheet) {
                [request finishWithPaths:nil error:@"请先关闭当前对话框，再选择上传项目"];
                return;
            }

            @try {
                NSOpenPanel *panel = [NSOpenPanel openPanel];
                request.panel = panel;
                panel.title = @"选择上传文件与文件夹";
                panel.prompt = @"选择";
                panel.canChooseFiles = YES;
                panel.canChooseDirectories = YES;
                panel.allowsMultipleSelection = YES;
                panel.showsHiddenFiles = YES;
                panel.resolvesAliases = NO;
                panel.treatsFilePackagesAsDirectories = YES;
                panel.canCreateDirectories = NO;
                // Keep the system sidebar, search, remembered directory and view
                // preferences. No accessory view or custom file browser is added.

                __weak SSHUploadPickerRequest *weakRequest = request;
                NSNotificationCenter *center = NSNotificationCenter.defaultCenter;
                request.closeObserver = [center addObserverForName:NSWindowWillCloseNotification
                    object:parent queue:NSOperationQueue.mainQueue usingBlock:^(NSNotification *note) {
                        [weakRequest cancel];
                    }];
                request.terminateObserver = [center addObserverForName:NSApplicationWillTerminateNotification
                    object:NSApp queue:NSOperationQueue.mainQueue usingBlock:^(NSNotification *note) {
                        [weakRequest cancel];
                    }];
                [panel beginSheetModalForWindow:parent completionHandler:^(NSModalResponse response) {
                    NSMutableArray<NSString *> *paths = [NSMutableArray new];
                    if (response == NSModalResponseOK) {
                        for (NSURL *url in request.panel.URLs) {
                            if (url.isFileURL && url.path) [paths addObject:url.path];
                        }
                    }
                    [request finishWithPaths:paths error:nil];
                }];
            } @catch (NSException *exception) {
                [request.panel orderOut:nil];
                [request finishWithPaths:nil error:@"无法打开系统文件选择窗口，请重试"];
            }
        }
    });
    // Only the bound RPC's background thread waits; the main queue handles the
    // sheet, cancellation and window-close completion asynchronously.
    dispatch_semaphore_wait(done, DISPATCH_TIME_FOREVER);
    return result;
}
