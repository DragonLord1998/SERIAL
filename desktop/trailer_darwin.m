//go:build darwin && cgo

#import <Cocoa/Cocoa.h>
#import <WebKit/WebKit.h>
#import <math.h>
#import "trailer_darwin.h"

static void OnMain(dispatch_block_t block) {
 if ([NSThread isMainThread]) block(); else dispatch_sync(dispatch_get_main_queue(),block);
}
static NSMutableDictionary<NSNumber *,id> *Players(void) {
 static NSMutableDictionary *players; static dispatch_once_t once;
 dispatch_once(&once,^{ players=[NSMutableDictionary new]; }); return players;
}
static NSMutableSet<NSNumber *> *StoppedTokens(void) {
 static NSMutableSet *tokens; static dispatch_once_t once;
 dispatch_once(&once,^{ tokens=[NSMutableSet new]; }); return tokens;
}
static BOOL ValidToken(uint64_t token) { return token>0 && token<=9007199254740991ULL; }
static BOOL ValidBounds(double x,double y,double w,double h) {
 return isfinite(x)&&isfinite(y)&&isfinite(w)&&isfinite(h)&&x>=0&&y>=0&&w>=200&&h>=200&&w<=8192&&h<=8192;
}
static BOOL VisibleBounds(WKWebView *host,double x,double y,double w,double h) {
 return host && host.window.isVisible && ValidBounds(x,y,w,h) && x+w<=NSWidth(host.bounds) && y+h<=NSHeight(host.bounds);
}
static NSRect Frame(WKWebView *host,double x,double y,double w,double h) {
 return NSMakeRect(NSMinX(host.bounds)+x,host.isFlipped ? NSMinY(host.bounds)+y : NSMaxY(host.bounds)-y-h,w,h);
}

@interface GoAnimeTrailer : NSObject <WKScriptMessageHandler, WKNavigationDelegate>
@property(nonatomic,strong) WKWebView *preview;
@property(nonatomic,weak) WKWebView *host;
@property(nonatomic,copy) NSString *videoID;
@property(nonatomic,copy) NSString *phase;
@property(nonatomic,copy) NSString *message;
@property(nonatomic,copy) NSString *desired;
@property(nonatomic) uint64_t token;
@property(nonatomic) double position;
@property(nonatomic) BOOL ready;
@property(nonatomic) BOOL restartPending;
@property(nonatomic) uint64_t commandRevision;
@property(nonatomic,strong) id clickMonitor;
- (void)dispose;
- (void)applyDesired;
- (void)pause;
- (void)armDeadline;
@end

@implementation GoAnimeTrailer
- (instancetype)init {
 if((self=[super init])) {
  self.phase=@"loading"; self.message=@""; self.desired=@"playing";
  [[NSNotificationCenter defaultCenter] addObserver:self selector:@selector(windowClosing:) name:NSWindowWillCloseNotification object:nil];
  [[NSNotificationCenter defaultCenter] addObserver:self selector:@selector(applicationTerminating:) name:NSApplicationWillTerminateNotification object:nil];
 }
 return self;
}
- (void)windowClosing:(NSNotification *)note { if(note.object==self.host.window) { [self dispose]; [StoppedTokens() addObject:@(self.token)]; [Players() removeObjectForKey:@(self.token)]; } }
- (void)applicationTerminating:(NSNotification *)note { [self dispose]; }
- (void)dispose {
 WKWebView *view=self.preview; self.preview=nil; self.host=nil;
 if(self.clickMonitor) { [NSEvent removeMonitor:self.clickMonitor]; self.clickMonitor=nil; }
 [[NSNotificationCenter defaultCenter] removeObserver:self];
 [view.configuration.userContentController removeScriptMessageHandlerForName:@"trailerStatus"];
 view.navigationDelegate=nil; [view stopLoading]; [view loadHTMLString:@"" baseURL:nil]; [view removeFromSuperview];
 self.phase=@"stopped";
}
- (void)applyDesired {
 if(!self.ready || !self.preview) return;
 NSString *command=[self.desired isEqualToString:@"paused"] ? @"pause" : (self.restartPending ? @"restart" : @"resume");
 self.restartPending=NO;
 [self.preview evaluateJavaScript:[NSString stringWithFormat:@"window.trailerCommand('%@');",command] completionHandler:nil];
}
- (void)armDeadline {
 uint64_t revision=self.commandRevision;
 __weak GoAnimeTrailer *weakSelf=self;
 dispatch_after(dispatch_time(DISPATCH_TIME_NOW,20*NSEC_PER_SEC),dispatch_get_main_queue(),^{
  GoAnimeTrailer *target=weakSelf;
  if(target.preview && target.commandRevision==revision && [target.desired isEqualToString:@"playing"] && [target.phase isEqualToString:@"loading"]) { target.phase=@"unavailable"; target.message=@"Trailer playback did not start"; }
 });
}
- (void)pause {
 self.commandRevision++;
 self.desired=@"paused"; self.preview.hidden=YES;
 if(![self.phase isEqualToString:@"unavailable"]) self.phase=@"paused";
 [self applyDesired];
}
- (void)userContentController:(WKUserContentController *)controller didReceiveScriptMessage:(WKScriptMessage *)message {
 if(!self.preview || message.webView!=self.preview || !message.frameInfo.mainFrame || ![message.body isKindOfClass:[NSDictionary class]]) return;
 NSDictionary *body=message.body;
 if(![body[@"token"] isKindOfClass:[NSNumber class]] || [body[@"token"] unsignedLongLongValue]!=self.token) return;
 if([body[@"position"] isKindOfClass:[NSNumber class]]) { double p=[body[@"position"] doubleValue]; if(isfinite(p)&&p>=0) self.position=p; }
 if([body[@"ready"] isKindOfClass:[NSNumber class]] && [body[@"ready"] boolValue]) { self.ready=YES; [self applyDesired]; }
 NSString *phase=body[@"phase"];
 if([@[@"loading",@"playing",@"paused",@"unavailable"] containsObject:phase]) {
  if([phase isEqualToString:@"unavailable"] || (![self.phase isEqualToString:@"unavailable"] && ![self.desired isEqualToString:@"paused"])) self.phase=phase;
  if(![self.phase isEqualToString:@"unavailable"] || [phase isEqualToString:@"unavailable"]) self.message=[body[@"message"] isKindOfClass:[NSString class]] ? body[@"message"] : @"";
 }
}
- (void)webView:(WKWebView *)webView didFailProvisionalNavigation:(WKNavigation *)navigation withError:(NSError *)error {
 if(webView==self.preview && error.code!=NSURLErrorCancelled) { self.phase=@"unavailable"; self.message=@"Trailer could not load"; }
}
- (void)webView:(WKWebView *)webView didFailNavigation:(WKNavigation *)navigation withError:(NSError *)error { [self webView:webView didFailProvisionalNavigation:navigation withError:error]; }
- (void)webViewWebContentProcessDidTerminate:(WKWebView *)webView { if(webView==self.preview) { self.phase=@"unavailable"; self.message=@"Trailer player stopped unexpectedly"; } }
@end

static WKWebView *FindHost(NSView *view) {
 if([view isKindOfClass:[WKWebView class]]) { WKWebView *web=(WKWebView *)view; if([web.URL.scheme isEqualToString:@"wails"]) return web; }
 for(NSView *child in view.subviews) { WKWebView *found=FindHost(child); if(found) return found; } return nil;
}
static BOOL InstalledOrigin(NSString **origin) {
 NSString *bundle=NSBundle.mainBundle.bundleIdentifier.lowercaseString;
 NSRegularExpression *valid=[NSRegularExpression regularExpressionWithPattern:@"^[a-z0-9][a-z0-9.-]{0,252}$" options:0 error:nil];
 if(!bundle.length || [valid numberOfMatchesInString:bundle options:0 range:NSMakeRange(0,bundle.length)]!=1) return NO;
 *origin=[@"https://" stringByAppendingString:bundle]; return YES;
}

char *goanime_trailer_start(const char *youtube_id,double x,double y,double w,double h,uint64_t token) {
 __block NSString *failure=nil;
 NSString *videoID=youtube_id ? [NSString stringWithUTF8String:youtube_id] : nil;
 OnMain(^{
  NSRegularExpression *valid=[NSRegularExpression regularExpressionWithPattern:@"^[A-Za-z0-9_-]{11}$" options:0 error:nil];
  if(!videoID || [valid numberOfMatchesInString:videoID options:0 range:NSMakeRange(0,videoID.length)]!=1 || !ValidToken(token) || !ValidBounds(x,y,w,h)) { failure=@"Invalid trailer viewport or identity"; return; }
  GoAnimeTrailer *existing=Players()[@(token)];
  if(existing) {
   if(![existing.videoID isEqualToString:videoID]) { failure=@"Trailer generation identity mismatch"; return; }
   if(!VisibleBounds(existing.host,x,y,w,h)) { [existing pause]; failure=@"Trailer viewport is not fully visible"; return; }
   existing.preview.frame=Frame(existing.host,x,y,w,h); existing.commandRevision++; existing.desired=@"playing"; existing.phase=@"loading"; existing.preview.hidden=NO; [existing applyDesired]; [existing armDeadline]; return;
  }
  if([StoppedTokens() containsObject:@(token)]) { failure=@"Stale trailer generation"; return; }
  WKWebView *host=nil; for(NSWindow *window in NSApp.windows) { host=FindHost(window.contentView); if(host) break; }
  if(!VisibleBounds(host,x,y,w,h)) { failure=@"Trailer viewport is not fully visible"; return; }
  NSString *origin=nil; if(!InstalledOrigin(&origin)) { failure=@"Installed application identity unavailable"; return; }
  GoAnimeTrailer *manager=[GoAnimeTrailer new]; manager.token=token; manager.videoID=videoID; manager.host=host;
  WKWebViewConfiguration *configuration=[WKWebViewConfiguration new];
  configuration.websiteDataStore=WKWebsiteDataStore.nonPersistentDataStore;
  configuration.mediaTypesRequiringUserActionForPlayback=WKAudiovisualMediaTypeNone;
  [configuration.userContentController addScriptMessageHandler:manager name:@"trailerStatus"];
  WKWebView *preview=[[WKWebView alloc] initWithFrame:Frame(host,x,y,w,h) configuration:configuration];
  if (@available(macOS 12.0, *)) preview.underPageBackgroundColor=NSColor.blackColor;
  manager.preview=preview; preview.navigationDelegate=manager; Players()[@(token)]=manager;
  [host addSubview:preview positioned:NSWindowAbove relativeTo:nil];
  __weak GoAnimeTrailer *weakManager=manager;
  manager.clickMonitor=[NSEvent addLocalMonitorForEventsMatchingMask:NSEventMaskLeftMouseDown handler:^NSEvent *(NSEvent *event) {
   GoAnimeTrailer *target=weakManager;
   if(!target || target.preview.hidden || event.window!=target.preview.window || ![target.phase isEqualToString:@"playing"]) return event;
   NSPoint point=[target.preview convertPoint:event.locationInWindow fromView:nil];
   NSRect bounds=target.preview.bounds;
   // Only the unobstructed central video area. YouTube top links and bottom
   // controls retain their original native events and branding.
   NSRect central=NSInsetRect(bounds,NSWidth(bounds)*0.2,NSHeight(bounds)*0.3);
   if(!NSPointInRect(point,central)) return event;
   NSString *script=[NSString stringWithFormat:@"window.dispatchEvent(new CustomEvent('native-trailer-click',{detail:{token:%llu,clickCount:%ld}}));",(unsigned long long)target.token,(long)event.clickCount];
   [target.host evaluateJavaScript:script completionHandler:nil]; return nil;
  }];
  // This app-identity HTTPS base URL supplies the required Referer to the
  // official iframe API, rather than using Wails' local document origin.
  NSString *html=[NSString stringWithFormat:@"<!doctype html><html><head><meta name='referrer' content='strict-origin-when-cross-origin'><meta name='viewport' content='width=device-width,initial-scale=1'><style>html,body,#player{margin:0;width:100%%;height:100%%;overflow:hidden;background:#000}</style></head><body><div id='player'></div><script>var player,ready=false;function status(phase,message,extra){var data={token:%llu,phase:phase,message:message||'',position:ready?player.getCurrentTime():0};if(extra)data.ready=true;window.webkit.messageHandlers.trailerStatus.postMessage(data);}window.trailerCommand=function(command){if(!ready)return;if(command==='pause'){player.pauseVideo();status('paused');}else{player.mute();if(command==='restart')player.seekTo(0,true);player.playVideo();}};function onYouTubeIframeAPIReady(){player=new YT.Player('player',{width:'100%%',height:'100%%',videoId:'%@',playerVars:{autoplay:0,playsinline:1,controls:1,enablejsapi:1,origin:'%@'},events:{onReady:function(e){e.target.mute();ready=true;status('loading','',true);},onStateChange:function(e){if(e.data===1)status('playing');else if(e.data===2||e.data===0)status('paused');},onError:function(e){var messages={100:'Trailer unavailable',101:'Trailer embedding disabled',150:'Trailer embedding disabled',153:'Trailer application identity rejected',2:'Invalid trailer',5:'Trailer playback unsupported'};status('unavailable',messages[e.data]||'Trailer playback failed');}}});}setInterval(function(){if(ready)status(player.getPlayerState()===1?'playing':player.getPlayerState()===2||player.getPlayerState()===0?'paused':'loading');},500);</script><script src='https://www.youtube.com/iframe_api'></script></body></html>",(unsigned long long)token,videoID,origin];
  [preview loadHTMLString:html baseURL:[NSURL URLWithString:origin]];
  [manager armDeadline];
 }); return failure ? strdup(failure.UTF8String) : NULL;
}
char *goanime_trailer_move(uint64_t token,double x,double y,double w,double h) {
 __block NSString *failure=nil;
 OnMain(^{ GoAnimeTrailer *manager=Players()[@(token)];
  if(!manager) { failure=@"Trailer preview not found"; return; }
  if(!VisibleBounds(manager.host,x,y,w,h)) { [manager pause]; failure=@"Trailer viewport is not fully visible"; return; }
  manager.preview.frame=Frame(manager.host,x,y,w,h);
 }); return failure ? strdup(failure.UTF8String) : NULL;
}
char *goanime_trailer_command(uint64_t token,const char *command) {
 __block NSString *failure=nil; NSString *action=command ? [NSString stringWithUTF8String:command] : nil;
 OnMain(^{ GoAnimeTrailer *manager=Players()[@(token)];
  if(!manager) { failure=@"Trailer preview not found"; return; }
  if(![@[@"pause",@"resume",@"restart"] containsObject:action]) { failure=@"Invalid trailer command"; return; }
  if([action isEqualToString:@"pause"]) { [manager pause]; return; }
  NSRect frame=manager.preview.frame; WKWebView *host=manager.host;
  if(!host || !host.window.isVisible || !NSContainsRect(host.bounds,frame)) { [manager pause]; failure=@"Trailer viewport is not fully visible"; return; }
  if([manager.phase isEqualToString:@"unavailable"]) { failure=manager.message; return; }
  manager.commandRevision++; manager.desired=@"playing"; manager.preview.hidden=NO;
  if([action isEqualToString:@"restart"]) manager.restartPending=YES;
  manager.phase=@"loading"; [manager applyDesired];
  [manager armDeadline];
 }); return failure ? strdup(failure.UTF8String) : NULL;
}
void goanime_trailer_stop(uint64_t token) {
 OnMain(^{ if(token==0) { NSArray *all=Players().allValues; [StoppedTokens() addObjectsFromArray:Players().allKeys]; [Players() removeAllObjects]; for(GoAnimeTrailer *player in all) [player dispose]; }
 else { if(ValidToken(token)) [StoppedTokens() addObject:@(token)]; GoAnimeTrailer *manager=Players()[@(token)]; [Players() removeObjectForKey:@(token)]; [manager dispose]; } });
}
char *goanime_trailer_status(uint64_t token) {
 __block char *result=NULL;
 OnMain(^{ GoAnimeTrailer *manager=Players()[@(token)];
  NSDictionary *status=@{@"token":@(token),@"phase":manager ? manager.phase : @"stopped",@"message":manager ? manager.message : @"",@"position":@(manager ? manager.position : 0)};
  NSData *data=[NSJSONSerialization dataWithJSONObject:status options:0 error:nil];
  if(data) result=strdup([[NSString alloc] initWithData:data encoding:NSUTF8StringEncoding].UTF8String);
 }); return result;
}
