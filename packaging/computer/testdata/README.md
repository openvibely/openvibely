# Browser media fixture

`h264-aac.mp4` is a one-second blue 64×64 video with a generated 440 Hz tone.
It contains H.264 video and AAC audio, with no third-party media content.
The image build uses it to verify codec detection and actual playback on each
architecture. FFmpeg is only needed to regenerate the fixture, not in the image.

Generated with Ubuntu 24.04's FFmpeg:

```sh
ffmpeg -nostdin -hide_banner -loglevel error \
  -f lavfi -i color=c=blue:size=64x64:rate=10 \
  -f lavfi -i sine=frequency=440:sample_rate=44100 \
  -t 1 -c:v libx264 -pix_fmt yuv420p -c:a aac \
  -movflags frag_keyframe+empty_moov -f mp4 h264-aac.mp4
```
