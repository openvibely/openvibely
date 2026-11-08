// Exercise an OS file drag through GTK into WebKit, not a synthetic DOM event.
#include "../window_controls_linux.c"
#include <webkit2/webkit2.h>
#include <gdk/gdkx.h>
#include <X11/extensions/XTest.h>
#include <signal.h>
#include <sys/wait.h>

void ovLinuxCaptionLayout(int left, int right) {}
static GtkWidget *window, *source, *view;
static char *file_uri;
static int result = 1, step;
static gboolean dragging;
static Display *display;
static int sx, sy, tx, ty;
static GPid source_pid;
static GtkWidget *source_window;
static const char *mode = "composer";

static void drag_begin(GtkWidget *widget, GdkDragContext *context, gpointer unused) {
 g_printerr("Native drag started\n");
}
static gboolean source_button(GtkWidget *widget, GdkEventButton *event, gpointer unused) {
 g_printerr("Source button pressed\n");
 return FALSE;
}
static void data_get(GtkWidget *widget, GdkDragContext *context, GtkSelectionData *selection,
                     guint info, guint time, gpointer data) {
 g_printerr("Supplying file URI: %s\n", file_uri);
 char *uris[] = {file_uri, NULL};
 gtk_selection_data_set_uris(selection, uris);
}
static void received(GtkWidget *widget, GdkDragContext *context, gint x, gint y, GtkSelectionData *data, guint info, guint time, gpointer unused) {
 gchar *target = gdk_atom_name(gtk_selection_data_get_target(data));
 g_printerr("Native received info=%u target=%s bytes=%d\n", info, target, gtk_selection_data_get_length(data));
 g_free(target);
}
static gboolean blocked_drop(GtkWidget *widget, GdkDragContext *context, gint x, gint y,
                             guint time, gpointer data) {
 gtk_drag_finish(context, FALSE, FALSE, time);
 return TRUE;
}
static gboolean blocked_motion(GtkWidget *widget, GdkDragContext *context, gint x, gint y,
                               guint time, gpointer data) {
 gdk_drag_status(context, 0, time);
 return TRUE;
}
static gboolean finish_pointer(gpointer unused) {
 webkit_web_view_evaluate_javascript(WEBKIT_WEB_VIEW(view), "window._ovNativeDropTestFinished = true", -1, NULL, NULL, NULL, NULL, NULL);
 return G_SOURCE_REMOVE;
}
static gboolean move_pointer(gpointer unused) {
 if (step == 0) {
  XTestFakeMotionEvent(display, -1, sx, sy, CurrentTime);
  XTestFakeButtonEvent(display, 1, True, CurrentTime);
 } else if (step <= 20) {
  XTestFakeMotionEvent(display, -1, sx + (tx-sx)*step/20, sy + (ty-sy)*step/20, CurrentTime);
 } else if (step == 25) {
  XTestFakeButtonEvent(display, 1, False, CurrentTime);
  XFlush(display);
  g_timeout_add(750, finish_pointer, NULL);
  return G_SOURCE_REMOVE;
 }
 XFlush(display);
 step++;
 return G_SOURCE_CONTINUE;
}
static void message(WebKitUserContentManager *manager, WebKitJavascriptResult *js, gpointer unused) {
 char *text = jsc_value_to_string(webkit_javascript_result_get_js_value(js));
 g_printerr("WebKit: %s\n", text);
 if (g_str_equal(text, "ready") && !dragging) {
  dragging = TRUE;
  int ox, oy, x, y;
  gdk_window_get_origin(gtk_widget_get_window(window), &ox, &oy);
  sx = 50; sy = 50;
  gtk_widget_translate_coordinates(view, window, 100, g_str_equal(mode, "outside") ? 350 : 110, &x, &y);
  tx = ox+x; ty = oy+y;
  g_printerr("Pointer: %d,%d -> %d,%d\n", sx, sy, tx, ty);
  g_timeout_add(50, move_pointer, NULL);
 } else if (g_str_equal(text, "pass")) {
  OVFileDrop *drop = g_object_get_data(G_OBJECT(view), "ov-file-drop");
  if (drop && (drop->choosing || drop->files)) {
   g_printerr("Native file grant survived the drop callback\n");
   gtk_main_quit();
   g_free(text);
   return;
  }
  result = 0;
  gtk_main_quit();
 } else if (g_str_has_prefix(text, "fail:")) {
  g_printerr("%s\n", text);
  gtk_main_quit();
 }
 g_free(text);
}
static gboolean unexpected_chooser(WebKitWebView *widget, WebKitFileChooserRequest *request, gpointer unused) {
 g_printerr("Unexpected file chooser without a native drop grant\n");
 webkit_file_chooser_request_cancel(request);
 gtk_main_quit();
 return TRUE;
}
static gboolean timeout(gpointer unused) {
 g_printerr("Timed out waiting for native PNG drop, upload, and preview\n");
 gtk_main_quit();
 return G_SOURCE_REMOVE;
}
static void create_source(GtkWidget *parent) {
 source = gtk_event_box_new();
 gtk_widget_set_size_request(source, 180, 500);
 gtk_container_add(GTK_CONTAINER(source), gtk_label_new("PNG file drag source"));
 gtk_container_add(GTK_CONTAINER(parent), source);
 GtkTargetEntry targets[] = {{"text/uri-list", 0, 0}};
 gtk_drag_source_set(source, GDK_BUTTON1_MASK, targets, 1, GDK_ACTION_COPY);
 g_signal_connect(source, "drag-data-get", G_CALLBACK(data_get), NULL);
 g_signal_connect(source, "drag-begin", G_CALLBACK(drag_begin), NULL);
 g_signal_connect(source, "button-press-event", G_CALLBACK(source_button), NULL);
 gtk_window_move(GTK_WINDOW(parent), 0, 0);
 gtk_widget_show_all(parent);
}
int main(int argc, char **argv) {
 if (argc != 3 && argc != 4) return 2;
 if (argc == 4) mode = argv[3];
 gboolean source_only = g_str_equal(argv[1], "--source");
 gtk_init(&argc, &argv);
 if (!GDK_IS_X11_DISPLAY(gdk_display_get_default())) return 3;
 display = gdk_x11_display_get_xdisplay(gdk_display_get_default());
 int a,b,c,d;
 if (!XTestQueryExtension(display, &a, &b, &c, &d)) return 4;
 file_uri = g_filename_to_uri(argv[2], NULL, NULL);
 if (source_only) {
  create_source(gtk_window_new(GTK_WINDOW_TOPLEVEL));
  gtk_main();
  return 0;
 }
 window = gtk_window_new(GTK_WINDOW_TOPLEVEL);
 gtk_window_set_default_size(GTK_WINDOW(window), 700, 500);
 gtk_window_move(GTK_WINDOW(window), 200, 0);
 GtkWidget *box = gtk_box_new(GTK_ORIENTATION_HORIZONTAL, 0);
 gtk_container_add(GTK_CONTAINER(window), box);
 if (g_str_equal(mode, "internal")) {
  // Same-process GTK drags (including WebKit HTML drags) are untrusted URI
  // text, even when they name a real local PNG. They must not gain a chooser grant.
  source_window = gtk_window_new(GTK_WINDOW_TOPLEVEL);
  create_source(source_window);
 } else {
  char *source_args[] = {argv[0], "--source", argv[2], NULL};
  if (!g_spawn_async(NULL, source_args, NULL, G_SPAWN_DO_NOT_REAP_CHILD, NULL, NULL, &source_pid, NULL)) return 5;
 }
 WebKitUserContentManager *manager = webkit_user_content_manager_new();
 webkit_user_content_manager_register_script_message_handler(manager, "test");
 g_signal_connect(manager, "script-message-received::test", G_CALLBACK(message), NULL);
 view = webkit_web_view_new_with_user_content_manager(manager);
 gtk_box_pack_start(GTK_BOX(box), view, TRUE, TRUE, 0);
 // Mirror Wails' disabled native-drop handlers, then run our production repair.
 g_signal_connect(view, "drag-drop", G_CALLBACK(blocked_drop), GUINT_TO_POINTER(1));
 g_signal_connect(view, "drag-motion", G_CALLBACK(blocked_motion), GUINT_TO_POINTER(1));
 ovEnableLinuxWebFileDrops(window, 1);
 g_signal_connect(view, "run-file-chooser", G_CALLBACK(unexpected_chooser), NULL);
 g_signal_connect(view, "drag-data-received", G_CALLBACK(received), NULL);
 gtk_widget_show_all(window);
 gtk_window_present(GTK_WINDOW(window));
 webkit_web_view_load_uri(WEBKIT_WEB_VIEW(view), argv[1]);
 g_timeout_add_seconds(15, timeout, NULL);
 gtk_main();
 XTestFakeButtonEvent(display, 1, False, CurrentTime);
 XFlush(display);
 if (source_pid) {
  kill(source_pid, SIGTERM);
  waitpid(source_pid, NULL, 0);
  g_spawn_close_pid(source_pid);
 }
 if (source_window) gtk_widget_destroy(source_window);
 gtk_widget_destroy(window);
 g_free(file_uri);
 g_object_unref(manager);
 return result;
}
