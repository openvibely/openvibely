#include "../window_controls_linux.c"
#if GTK_MAJOR_VERSION < 4
#include <webkit2/webkit2.h>
#else
#include <webkit/webkit.h>
#endif

static int reported_left, reported_right, close_count;
void ovLinuxCaptionLayout(int left, int right) { reported_left=left; reported_right=right; }
static gboolean closing(GtkWidget *widget, gpointer a, gpointer b) { close_count++; return TRUE; }
static void settle(void) {
 for (int i=0;i<100;i++) {
  while (g_main_context_iteration(NULL,FALSE));
  g_usleep(5000);
 }
}
#if GTK_MAJOR_VERSION < 4
static void collect_child(GtkWidget *child,gpointer data) { *(GList**)data=g_list_append(*(GList**)data,child); }
#endif
static GtkWidget *find_button(GtkWidget *root, const char *style) {
#if GTK_MAJOR_VERSION < 4
 if (GTK_IS_BUTTON(root) && gtk_style_context_has_class(gtk_widget_get_style_context(root),style)) return root;
 if (!GTK_IS_CONTAINER(root)) return NULL;
 GList *children=NULL;
 gtk_container_forall(GTK_CONTAINER(root),collect_child,&children);
 GtkWidget *found=NULL;
 for (GList *p=children;p && !found;p=p->next) found=find_button(p->data,style);
 g_list_free(children);
 return found;
#else
 if (GTK_IS_BUTTON(root) && gtk_widget_has_css_class(root,style)) return root;
 for (GtkWidget *child=gtk_widget_get_first_child(root);child;child=gtk_widget_get_next_sibling(child)) {
  GtkWidget *found=find_button(child,style); if (found) return found;
 }
 return NULL;
#endif
}
static gboolean corners_match(GtkWidget *window, gboolean rounded) {
#if GTK_MAJOR_VERSION < 4
 // Match the production fallback on desktops without alpha compositing.
 rounded = rounded && gdk_screen_is_composited(gtk_widget_get_screen(window)) &&
  gtk_widget_get_visual(window) == gdk_screen_get_rgba_visual(gtk_widget_get_screen(window));
#endif
 int width = gtk_widget_get_allocated_width(window);
 int height = gtk_widget_get_allocated_height(window);
 cairo_surface_t *surface = cairo_image_surface_create(CAIRO_FORMAT_ARGB32, width, height);
 cairo_t *cr = cairo_create(surface);
#if GTK_MAJOR_VERSION < 4
 gtk_widget_draw(window, cr);
#else
 GdkPaintable *paintable = gtk_widget_paintable_new(window);
 GtkSnapshot *snapshot = gtk_snapshot_new();
 gdk_paintable_snapshot(paintable, snapshot, width, height);
 GskRenderNode *node = gtk_snapshot_free_to_node(snapshot);
 if (node) { gsk_render_node_draw(node, cr); gsk_render_node_unref(node); }
 g_object_unref(paintable);
#endif
 cairo_destroy(cr);
 cairo_surface_flush(surface);
 unsigned char *pixels = cairo_image_surface_get_data(surface);
 int stride = cairo_image_surface_get_stride(surface);
 gboolean matches = TRUE;
 int points[][2] = {{0,0}, {width-1,0}, {0,height-1}, {width-1,height-1}};
 for (int i=0;i<4;i++) {
  guint32 pixel = *(guint32 *)(pixels + points[i][1]*stride + points[i][0]*4);
  if ((pixel >> 24) != (rounded ? 0 : 255)) matches = FALSE;
 }
 // The content must still paint fully opaque away from the clipped corners.
 if ((*(guint32 *)(pixels + (height/2)*stride + (width/2)*4) >> 24) != 255) matches = FALSE;
 cairo_surface_destroy(surface);
 if (!matches) g_printerr("corner pixels did not match rounded=%d\n", rounded);
 return matches;
}

int main(int argc,char **argv) {
#if GTK_MAJOR_VERSION < 4
 gtk_init(&argc,&argv);
 GtkWidget *window=gtk_window_new(GTK_WINDOW_TOPLEVEL);
 GtkWidget *content=webkit_web_view_new();
 gtk_container_add(GTK_CONTAINER(window),content);
 g_signal_connect(window,"delete-event",G_CALLBACK(closing),NULL);
#else
 gtk_init();
 GtkWidget *window=gtk_window_new();
 GtkWidget *content=webkit_web_view_new();
 gtk_window_set_child(GTK_WINDOW(window),content);
 g_signal_connect(window,"close-request",G_CALLBACK(closing),NULL);
#endif
 gtk_window_set_decorated(GTK_WINDOW(window),FALSE);
#if GTK_MAJOR_VERSION < 4
 GdkVisual *visual = gdk_screen_get_rgba_visual(gtk_widget_get_screen(window));
 if (visual) gtk_widget_set_visual(window, visual);
 gtk_widget_set_app_paintable(window, TRUE);
#endif
 // Use a real, opaque WebKit surface to catch corner clipping that works for
 // ordinary GTK widgets but is bypassed by the webview's renderer.
 webkit_web_view_load_html(WEBKIT_WEB_VIEW(content), "<html style='background:#234567'><body>Shared header and webview content</body></html>", NULL);
 gtk_window_set_default_size(GTK_WINDOW(window),800,400);
 g_object_set(gtk_settings_get_default(),"gtk-decoration-layout",":minimize,maximize,close",NULL);
 ovInstallLinuxCaption(window);
 gtk_window_present(GTK_WINDOW(window)); settle();
 OVCaption *c=g_object_get_data(G_OBJECT(window),"ov-caption");
 if (!c || !gtk_widget_get_parent(content)) return 10;
 if (!corners_match(window, TRUE)) return 23;
#if GTK_MAJOR_VERSION < 4
 // Wails shows the hierarchy after the caption is installed. Empty groups
 // must remain hidden, otherwise their padding shifts the sidebar toggle.
 gtk_widget_show_all(window); settle();
 if (gtk_widget_get_visible(c->left)) return 21;
#endif
 if (reported_left != 0 || reported_right < 60) { g_printerr("right layout %d %d\n",reported_left,reported_right);return 11; }
 if (!find_button(c->right,"minimize") || !find_button(c->right,"maximize") || !find_button(c->right,"close")) return 12;
 // GTK's default can request an app menu even when this window has none.
 // A nonempty layout string is not evidence of an actual left-side control.
 g_object_set(gtk_settings_get_default(),"gtk-decoration-layout","menu:minimize,maximize,close",NULL);
 settle();
 if (reported_left != 0) { g_printerr("phantom menu width %d\n",reported_left);return 22; }
 g_object_set(gtk_settings_get_default(),"gtk-decoration-layout","close,minimize,maximize:",NULL);
 settle();
 if (reported_left < 60 || reported_right != 0) { g_printerr("left layout %d %d\n",reported_left,reported_right);return 13; }
 GtkWidget *maximize=find_button(c->left,"maximize");
 if (!maximize) return 14;
 g_signal_emit_by_name(maximize,"clicked"); settle();
 if (!gtk_window_is_maximized(GTK_WINDOW(window))) return 15;
 if (!corners_match(window, FALSE)) return 24;
 // Controls should follow the native window state and offer restore.
 maximize=find_button(c->left,"maximize");
 if (!maximize) return 16;
 g_signal_emit_by_name(maximize,"clicked"); settle();
 if (gtk_window_is_maximized(GTK_WINDOW(window))) return 17;
 if (!corners_match(window, TRUE)) return 25;
 gtk_window_fullscreen(GTK_WINDOW(window)); settle();
 if (!corners_match(window, FALSE)) return 26;
 gtk_window_unfullscreen(GTK_WINDOW(window)); settle();
 if (!corners_match(window, TRUE)) return 27;
 g_object_set(gtk_settings_get_default(),"gtk-decoration-layout","",NULL);
 settle();
 if (reported_left != 0 || reported_right != 0) return 20;
 g_object_set(gtk_settings_get_default(),"gtk-decoration-layout","close,minimize,maximize:",NULL);
 ovSetLinuxCaptionDark(1);
 settle();
 if (!find_button(c->left,"close")) return 18;
 g_signal_emit_by_name(find_button(c->left,"close"),"clicked"); settle();
 if (close_count != 1) return 19;
#if GTK_MAJOR_VERSION < 4
 gtk_widget_destroy(window);
#else
 gtk_window_destroy(GTK_WINDOW(window));
#endif
 return 0;
}
