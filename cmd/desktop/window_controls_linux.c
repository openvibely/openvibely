//go:build linux

#include <gtk/gtk.h>
#include "window_controls_linux.h"

typedef struct {
 GtkWidget *window, *left, *right, *overlay;
 guint pending_frames;
 int last_left, last_right;
} OVCaption;

#if GTK_MAJOR_VERSION < 4
static void ovCaptionFindButton(GtkWidget *widget, gpointer data) {
 gboolean *found = data;
 if (*found || !gtk_widget_get_visible(widget)) return;
 if (GTK_IS_BUTTON(widget)) { *found = TRUE; return; }
 if (GTK_IS_CONTAINER(widget)) gtk_container_forall(GTK_CONTAINER(widget), ovCaptionFindButton, data);
}
static int ovCaptionWidth(GtkWidget *widget) {
 gboolean found = FALSE;
 ovCaptionFindButton(widget, &found);
 // A "menu" decoration without an application menu leaves an empty padded
 // header bar. Only reserve space when GTK actually created a button.
 return found ? gtk_widget_get_allocated_width(widget) : 0;
}
#endif

static void ovCaptionMeasure(OVCaption *c) {
#if GTK_MAJOR_VERSION < 4
 int left = ovCaptionWidth(c->left);
 int right = ovCaptionWidth(c->right);
#else
 int left = gtk_window_controls_get_empty(GTK_WINDOW_CONTROLS(c->left)) ? 0 : gtk_widget_get_width(c->left) + 6;
 int right = gtk_window_controls_get_empty(GTK_WINDOW_CONTROLS(c->right)) ? 0 : gtk_widget_get_width(c->right) + 6;
#endif
 if (left != c->last_left || right != c->last_right) {
  c->last_left = left; c->last_right = right;
  ovLinuxCaptionLayout(left, right);
 }
}

#if GTK_MAJOR_VERSION < 4
static void ovCaptionStateChanged(GtkWidget *widget, GtkStateFlags previous, gpointer data) {
 // The transparent caption sits over WebKit's separately rendered surface.
 // Redraw the overlay as well as the button so the old hover is erased.
 gtk_widget_queue_draw(GTK_WIDGET(data));
}
static void ovCaptionWatchState(GtkWidget *widget, gpointer data) {
 if (!g_object_get_data(G_OBJECT(widget), "ov-caption-state-watched")) {
  g_signal_connect(widget, "state-flags-changed", G_CALLBACK(ovCaptionStateChanged), data);
  g_object_set_data(G_OBJECT(widget), "ov-caption-state-watched", GINT_TO_POINTER(1));
 }
 if (GTK_IS_CONTAINER(widget)) gtk_container_forall(GTK_CONTAINER(widget), ovCaptionWatchState, data);
}
static void ovCaptionAllocated(GtkWidget *widget, GtkAllocation *allocation, gpointer data) {
 OVCaption *c = data;
 ovCaptionWatchState(widget, c->overlay);
 ovCaptionMeasure(c);
}
static void ovCaptionSettings(GtkSettings *settings, GParamSpec *spec, gpointer data) {
 OVCaption *c = data;
 gchar *layout = NULL;
 g_object_get(settings, "gtk-decoration-layout", &layout, NULL);
 gchar **parts = g_strsplit(layout ? layout : ":minimize,maximize,close", ":", 2);
 const gchar *start = parts[0] ? parts[0] : "";
 const gchar *end = parts[0] && parts[1] ? parts[1] : "";
 gchar *left = g_strconcat(start, ":", NULL);
 gchar *right = g_strconcat(":", end, NULL);
 gtk_header_bar_set_decoration_layout(GTK_HEADER_BAR(c->left), left);
 gtk_header_bar_set_decoration_layout(GTK_HEADER_BAR(c->right), right);
 gtk_widget_set_visible(c->left, start[0] != '\0');
 gtk_widget_set_visible(c->right, end[0] != '\0');
 g_free(left); g_free(right); g_strfreev(parts); g_free(layout);
 ovCaptionMeasure(c);
}
#else
// Measure after GTK has laid out a changed theme/state. Stop the callback
// after two frames rather than keeping an idle desktop rendering at 60fps.
static gboolean ovCaptionTick(GtkWidget *widget, GdkFrameClock *clock, gpointer data) {
 OVCaption *c = data;
 ovCaptionMeasure(c);
 return --c->pending_frames ? G_SOURCE_CONTINUE : G_SOURCE_REMOVE;
}
static void ovCaptionChanged(GObject *object, GParamSpec *spec, gpointer data) {
 OVCaption *c = data;
 if (!c->pending_frames) gtk_widget_add_tick_callback(c->overlay, ovCaptionTick, c, NULL);
 c->pending_frames = 2;
}
#endif

static void ovCaptionFree(gpointer data) {
 OVCaption *c = data;
 g_signal_handlers_disconnect_by_data(gtk_settings_get_default(), c);
 g_free(c);
}

#if GTK_MAJOR_VERSION < 4
static gboolean ovWindowDraw(GtkWidget *window, cairo_t *cr, gpointer data) {
 GdkWindow *surface = gtk_widget_get_window(window);
 GdkWindowState state = gdk_window_get_state(surface);
 gboolean square = (state & (GDK_WINDOW_STATE_MAXIMIZED | GDK_WINDOW_STATE_FULLSCREEN | GDK_WINDOW_STATE_TILED |
  GDK_WINDOW_STATE_TOP_TILED | GDK_WINDOW_STATE_RIGHT_TILED | GDK_WINDOW_STATE_BOTTOM_TILED | GDK_WINDOW_STATE_LEFT_TILED)) ||
  !gdk_screen_is_composited(gtk_widget_get_screen(window)) ||
  gtk_widget_get_visual(window) != gdk_screen_get_rgba_visual(gtk_widget_get_screen(window));
 double width = gtk_widget_get_allocated_width(window);
 double height = gtk_widget_get_allocated_height(window);
 double radius = square ? 0 : MIN(12, MIN(width, height) / 2);
 // Clear the previous frame first, including pixels exposed after restoring
 // from maximized. GTK3 doesn't clip child drawing to CSS border-radius.
 cairo_save(cr);
 cairo_set_operator(cr, CAIRO_OPERATOR_CLEAR);
 cairo_paint(cr);
 cairo_set_operator(cr, CAIRO_OPERATOR_OVER);
 cairo_new_path(cr);
 if (radius > 0) {
  cairo_arc(cr, width-radius, radius, radius, -G_PI/2, 0);
  cairo_arc(cr, width-radius, height-radius, radius, 0, G_PI/2);
  cairo_arc(cr, radius, height-radius, radius, G_PI/2, G_PI);
  cairo_arc(cr, radius, radius, radius, G_PI, 3*G_PI/2);
  cairo_close_path(cr);
 } else {
  cairo_rectangle(cr, 0, 0, width, height);
 }
 cairo_clip(cr);
 // Invoke the default renderer once under the clip, including the WebKit
 // surface and native caption overlay. Returning TRUE stops a second draw.
 GTK_WIDGET_GET_CLASS(window)->draw(window, cr);
 cairo_restore(cr);
 return TRUE;
}

static gboolean ovWindowState(GtkWidget *window, GdkEventWindowState *event, gpointer data) {
 gtk_widget_queue_draw(window);
 return FALSE;
}
#endif

static void ovInstallWindowCorners(GtkWidget *window) {
#if GTK_MAJOR_VERSION < 4
 g_signal_connect(window, "draw", G_CALLBACK(ovWindowDraw), NULL);
 g_signal_connect(window, "window-state-event", G_CALLBACK(ovWindowState), NULL);
#else
 // GTK4 clips the entire child snapshot (including WebKit) to the rounded
 // CSS padding box. Window-state classes remove the clip at screen edges.
 GtkCssProvider *css = gtk_css_provider_new();
 gtk_css_provider_load_from_data(css,
  "window.ov-rounded { border-radius: 12px; }"
  "window.ov-rounded.maximized, window.ov-rounded.fullscreen, window.ov-rounded.tiled,"
  "window.ov-rounded.tiled-top, window.ov-rounded.tiled-right,"
  "window.ov-rounded.tiled-bottom, window.ov-rounded.tiled-left { border-radius: 0; }", -1);
 gtk_style_context_add_provider(gtk_widget_get_style_context(window), GTK_STYLE_PROVIDER(css), GTK_STYLE_PROVIDER_PRIORITY_APPLICATION);
 g_object_unref(css);
 gtk_widget_add_css_class(window, "ov-rounded");
 gtk_widget_set_overflow(window, GTK_OVERFLOW_HIDDEN);
#endif
}

void ovInstallLinuxCaption(void *native_window) {
 if (!native_window || !GTK_IS_WINDOW(native_window)) return;
 GtkWidget *window = GTK_WIDGET(native_window);
 if (g_object_get_data(G_OBJECT(window), "ov-caption")) return;
 ovInstallWindowCorners(window);
 OVCaption *c = g_new0(OVCaption, 1);
 c->window = window; c->last_left = c->last_right = -1;
 GtkWidget *overlay = gtk_overlay_new();
 c->overlay = overlay;
#if GTK_MAJOR_VERSION < 4
 GtkWidget *content = gtk_bin_get_child(GTK_BIN(window));
 g_object_ref(content);
 gtk_container_remove(GTK_CONTAINER(window), content);
 gtk_container_add(GTK_CONTAINER(overlay), content);
 g_object_unref(content);
 gtk_container_add(GTK_CONTAINER(window), overlay);
 c->left = gtk_header_bar_new(); c->right = gtk_header_bar_new();
 GtkCssProvider *css = gtk_css_provider_new();
 gtk_css_provider_load_from_data(css, "#ov-caption-left, #ov-caption-right { background: transparent; background-image: none; border: none; box-shadow: none; padding: 0 6px; margin: 0; min-height: 0; }", -1, NULL);
 GtkWidget *controls[] = {c->left,c->right};
 for (int i=0;i<2;i++) {
  // Wails may show the window hierarchy again after installation. Keep an
  // empty decoration group hidden so it cannot reserve phantom header space.
  gtk_widget_set_no_show_all(controls[i], TRUE);
  gtk_header_bar_set_has_subtitle(GTK_HEADER_BAR(controls[i]), FALSE);
  gtk_header_bar_set_custom_title(GTK_HEADER_BAR(controls[i]), gtk_box_new(GTK_ORIENTATION_HORIZONTAL,0));
  gtk_header_bar_set_show_close_button(GTK_HEADER_BAR(controls[i]), TRUE);
  gtk_widget_set_name(controls[i], i ? "ov-caption-right" : "ov-caption-left");
  gtk_style_context_add_provider(gtk_widget_get_style_context(controls[i]), GTK_STYLE_PROVIDER(css), GTK_STYLE_PROVIDER_PRIORITY_APPLICATION);
  g_signal_connect(controls[i], "size-allocate", G_CALLBACK(ovCaptionAllocated), c);
 }
 g_object_unref(css);
#else
 GtkWidget *content = gtk_window_get_child(GTK_WINDOW(window));
 g_object_ref(content);
 gtk_window_set_child(GTK_WINDOW(window), NULL);
 gtk_overlay_set_child(GTK_OVERLAY(overlay), content);
 g_object_unref(content);
 gtk_window_set_child(GTK_WINDOW(window), overlay);
 c->left = gtk_window_controls_new(GTK_PACK_START);
 c->right = gtk_window_controls_new(GTK_PACK_END);
 gtk_widget_set_margin_start(c->left, 6);
 gtk_widget_set_margin_end(c->right, 6);
 g_signal_connect(gtk_settings_get_default(), "notify", G_CALLBACK(ovCaptionChanged), c);
 g_signal_connect(window, "notify::maximized", G_CALLBACK(ovCaptionChanged), c);
 g_signal_connect(window, "notify::fullscreened", G_CALLBACK(ovCaptionChanged), c);
 g_signal_connect(c->left, "notify::empty", G_CALLBACK(ovCaptionChanged), c);
 g_signal_connect(c->right, "notify::empty", G_CALLBACK(ovCaptionChanged), c);
 ovCaptionChanged(NULL, NULL, c);
#endif
 gtk_widget_set_direction(c->left, GTK_TEXT_DIR_LTR);
 gtk_widget_set_direction(c->right, GTK_TEXT_DIR_LTR);
 gtk_widget_set_halign(c->left, GTK_ALIGN_START);
 gtk_widget_set_halign(c->right, GTK_ALIGN_END);
 gtk_widget_set_valign(c->left, GTK_ALIGN_START);
 gtk_widget_set_valign(c->right, GTK_ALIGN_START);
 gtk_widget_set_size_request(c->left, -1, 46);
 gtk_widget_set_size_request(c->right, -1, 46);
 gtk_overlay_add_overlay(GTK_OVERLAY(overlay), c->left);
 gtk_overlay_add_overlay(GTK_OVERLAY(overlay), c->right);
 g_object_set_data_full(G_OBJECT(window), "ov-caption", c, ovCaptionFree);
#if GTK_MAJOR_VERSION < 4
 gtk_widget_show_all(overlay);
 g_signal_connect(gtk_settings_get_default(), "notify::gtk-decoration-layout", G_CALLBACK(ovCaptionSettings), c);
 ovCaptionSettings(gtk_settings_get_default(), NULL, c);
#else
 gtk_widget_set_visible(overlay, TRUE);
#endif
}

void ovSetLinuxCaptionDark(int dark) {
 g_object_set(gtk_settings_get_default(), "gtk-application-prefer-dark-theme", dark != 0, NULL);
}
