//go:build openvibely_native_ui && gtk3

package main

/*
#cgo pkg-config: gtk+-3.0 webkit2gtk-4.1
#include <gtk/gtk.h>
#include <webkit2/webkit2.h>
static GtkWidget* findWebView(GtkWidget* node) {
 if(WEBKIT_IS_WEB_VIEW(node))return node;
 if(!GTK_IS_CONTAINER(node))return NULL;
 GList* children=gtk_container_get_children(GTK_CONTAINER(node));GtkWidget* result=NULL;
 for(GList* it=children;it && !result;it=it->next)result=findWebView(GTK_WIDGET(it->data));
 g_list_free(children);return result;
}
static void sendWheel(void* pointer,double x,double y,double dx,double dy) {
 GtkWidget* view=findWebView(GTK_WIDGET(pointer));if(!view)return;
 GdkEvent* event=gdk_event_new(GDK_SCROLL);
 event->scroll.window=g_object_ref(gtk_widget_get_window(view));
 event->scroll.time=GDK_CURRENT_TIME;event->scroll.x=x;event->scroll.y=y;
 event->scroll.direction=GDK_SCROLL_SMOOTH;
 event->scroll.delta_x=dx/40.0;event->scroll.delta_y=dy/40.0;
 GdkSeat* seat=gdk_display_get_default_seat(gtk_widget_get_display(view));
 gdk_event_set_device(event,gdk_seat_get_pointer(seat));
 gtk_widget_event(view,event);gdk_event_free(event);
}
*/
import "C"
import "unsafe"

func nativeWheel(window unsafe.Pointer, e wheel) {
	C.sendWheel(window, C.double(e.X), C.double(e.Y), C.double(e.DX), C.double(e.DY))
}
