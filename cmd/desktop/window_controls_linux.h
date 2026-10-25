#ifndef OV_LINUX_CAPTION_H
#define OV_LINUX_CAPTION_H
void ovInstallLinuxCaption(void *window);
void ovEnableLinuxWebFileDrops(void *window, unsigned int window_id);
void ovSetLinuxCaptionHidden(void *window, int hidden);
void ovSetLinuxCaptionDark(int dark);
extern void ovLinuxCaptionLayout(int left, int right);
#endif
