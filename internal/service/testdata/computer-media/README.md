# Opt-in project computer codec check

Normal `go test ./...` runs skip this test. Docker image builds do not run it.
No CI workflow is added. Build the image explicitly, then opt in:

```sh
docker build -f packaging/computer/Dockerfile -t ov-computer-test .
OPENVIBELY_TEST_COMPUTER_IMAGE=ov-computer-test \
  go test ./internal/service -run '^TestProjectComputerDockerMediaCodecs$' -count=1 -v
```

The test copies these fixtures into a new container from the supplied local
image, disables networking, verifies H.264/AAC support and playback, and removes
the container. It never builds or pulls an image or connects to a running desktop.
These files are not part of the app's embedded computer-image build context.
Setting the same variable for `go test ./...` also enables the other existing
live computer-image tests.

`h264-aac.mp4` is a generated one-second blue 64×64 video with a 440 Hz tone.
It contains no third-party media. Regenerate with FFmpeg:

```sh
ffmpeg -nostdin -hide_banner -loglevel error \
  -f lavfi -i color=c=blue:size=64x64:rate=10 \
  -f lavfi -i sine=frequency=440:sample_rate=44100 \
  -t 1 -c:v libx264 -pix_fmt yuv420p -c:a aac \
  -movflags frag_keyframe+empty_moov -f mp4 h264-aac.mp4
```
