// WebKitGTK disables DataTransfer file access (WebKit bug 323277). Keep the
// filesystem grant native: feed a genuine external drop into WebKit's file
// chooser API, then let the composer's existing change/upload handler run.
#ifndef OV_FILE_DROP_GTK3_H
#define OV_FILE_DROP_GTK3_H
#include <webkit2/webkit2.h>

typedef struct {
 GdkDragContext *context;
 gchar **files;
 gboolean choosing;
} OVFileDrop;

static void ovFileDropClear(OVFileDrop *drop) {
 g_clear_object(&drop->context);
 g_clear_pointer(&drop->files, g_strfreev);
 drop->choosing = FALSE;
}
static void ovFileDropFree(gpointer data) {
 ovFileDropClear(data);
 g_free(data);
}
static void ovFileDropEvaluated(GObject *object, GAsyncResult *result, gpointer data) {
 GError *error = NULL;
 JSCValue *value = webkit_web_view_evaluate_javascript_finish(WEBKIT_WEB_VIEW(object), result, &error);
 g_clear_object(&value);
 g_clear_error(&error);
 OVFileDrop *drop = g_object_get_data(object, "ov-file-drop");
 // Never leave a grant available for a later, unrelated file picker.
 if (drop && drop->context == data) ovFileDropClear(drop);
 g_object_unref(data);
}
static gboolean ovFileDropChooser(WebKitWebView *view, WebKitFileChooserRequest *request, gpointer data) {
 OVFileDrop *drop = data;
 if (!drop->choosing || !drop->files) return FALSE;
 drop->choosing = FALSE;
 webkit_file_chooser_request_select_files(request, (const gchar * const *)drop->files);
 return TRUE;
}
static void ovFileDropData(GtkWidget *widget, GdkDragContext *context, gint x, gint y,
                           GtkSelectionData *selection, guint info, guint time, gpointer data) {
 OVFileDrop *drop = data;
 // Internal HTML drags can fabricate file:// text. They must never grant access
 // to local files. Accept URI lists only from an external native drag source.
 if (gtk_drag_get_source_widget(context)) return;
 if (gtk_selection_data_get_target(selection) != gdk_atom_intern_static_string("text/uri-list")) return;
 ovFileDropClear(drop);
 gchar **uris = gtk_selection_data_get_uris(selection);
 if (!uris) return;
 GPtrArray *files = g_ptr_array_new_with_free_func(g_free);
 for (guint i = 0; uris[i]; i++) {
  gchar *host = NULL;
  gchar *path = g_filename_from_uri(uris[i], &host, NULL);
  if (path && (!host || !*host || !g_ascii_strcasecmp(host, "localhost")) && g_file_test(path, G_FILE_TEST_IS_REGULAR))
   g_ptr_array_add(files, path);
  else
   g_free(path);
  g_free(host);
 }
 g_strfreev(uris);
 if (files->len) {
  g_ptr_array_add(files, NULL);
  drop->files = (gchar **)g_ptr_array_free(files, FALSE);
  drop->context = g_object_ref(context);
  webkit_web_view_evaluate_javascript(WEBKIT_WEB_VIEW(widget), "window._ovNativeFileDrag = true", -1, NULL, NULL, NULL, NULL, NULL);
 } else {
  g_ptr_array_free(files, TRUE);
 }
}
static gboolean ovFileDropReceived(GtkWidget *widget, GdkDragContext *context, gint x, gint y,
                                   guint time, gpointer data) {
 OVFileDrop *drop = data;
 if (drop->context != context || !drop->files) return FALSE;
 drop->choosing = TRUE;
 // GTK reports widget pixels; elementFromPoint expects CSS pixels.
 double zoom = webkit_web_view_get_zoom_level(WEBKIT_WEB_VIEW(widget));
 gchar x_text[G_ASCII_DTOSTR_BUF_SIZE], y_text[G_ASCII_DTOSTR_BUF_SIZE];
 g_ascii_dtostr(x_text, sizeof x_text, x / zoom);
 g_ascii_dtostr(y_text, sizeof y_text, y / zoom);
 gchar *script = g_strdup_printf("window._ovNativeFileDrag = false; window.ovAcceptNativeFileDrop && window.ovAcceptNativeFileDrop(%s, %s)", x_text, y_text);
 webkit_web_view_evaluate_javascript(WEBKIT_WEB_VIEW(widget), script, -1, NULL, NULL, NULL, ovFileDropEvaluated, g_object_ref(context));
 g_free(script);
 gtk_drag_finish(context, TRUE, FALSE, time);
 return TRUE;
}
static void ovFileDropLeave(GtkWidget *widget, GdkDragContext *context, guint time, gpointer data) {
 // GTK sends leave immediately before drop; keep the native grant until the
 // drop callback or the next drag, but it cannot be used by a chooser yet.
 webkit_web_view_evaluate_javascript(WEBKIT_WEB_VIEW(widget), "window._ovNativeFileDrag = false", -1, NULL, NULL, NULL, NULL, NULL);
}
static void ovInstallFileDropBridge(GtkWidget *widget) {
 if (g_object_get_data(G_OBJECT(widget), "ov-file-drop")) return;
 OVFileDrop *drop = g_new0(OVFileDrop, 1);
 g_object_set_data_full(G_OBJECT(widget), "ov-file-drop", drop, ovFileDropFree);
 g_signal_connect(widget, "drag-data-received", G_CALLBACK(ovFileDropData), drop);
 g_signal_connect(widget, "drag-drop", G_CALLBACK(ovFileDropReceived), drop);
 g_signal_connect(widget, "drag-leave", G_CALLBACK(ovFileDropLeave), drop);
 g_signal_connect(widget, "run-file-chooser", G_CALLBACK(ovFileDropChooser), drop);
}
#endif
