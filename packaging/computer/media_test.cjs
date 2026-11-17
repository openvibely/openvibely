// Runs during the computer image build; requires no desktop or network access.
const {test} = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const {chromium} = require('/opt/browser/node_modules/playwright');

test('bundled browser decodes H.264 video and AAC audio', {timeout: 30000}, async () => {
  const browser = await chromium.launch({
    executablePath: chromium.executablePath(),
    headless: true,
    args: ['--no-sandbox', '--disable-dev-shm-usage'],
  });
  try {
    const page = await browser.newPage();
    const media = fs.readFileSync(path.join(__dirname, 'testdata/h264-aac.mp4')).toString('base64');
    const result = await page.evaluate(async base64 => {
      const types = ['video/mp4; codecs="avc1.42E01E"', 'audio/mp4; codecs="mp4a.40.2"'];
      const support = types.map(type => ({type,
        playback: document.createElement('video').canPlayType(type),
        streaming: MediaSource.isTypeSupported(type),
      }));
      const src = 'data:video/mp4;base64,' + base64;
      // Exercise the audio track independently, as muted video alone could hide
      // an audio decoding failure. No speakers are required for decoding.
      async function play(tag) {
        const element = document.createElement(tag);
        element.muted = true;
        document.body.append(element);
        try {
          return await new Promise((resolve, reject) => {
            const timer = setTimeout(() => reject(new Error(tag + ' playback timed out')), 10000);
            const fail = error => {clearTimeout(timer); reject(error);};
            element.onended = () => {
              clearTimeout(timer);
              resolve({tag, time: element.currentTime, width: element.videoWidth,
                frames: tag === 'video' ? element.getVideoPlaybackQuality().totalVideoFrames : undefined});
            };
            element.onerror = () => fail(new Error(tag + ': ' + element.error?.message));
            element.src = src;
            element.play().catch(fail);
          });
        } finally {
          element.pause();
          element.removeAttribute('src');
          element.load();
          element.remove();
        }
      }
      return {support, video: await play('video'), audio: await play('audio')};
    }, media);
    for (const codec of result.support) {
      assert.notEqual(codec.playback, '', codec.type + ' playback unsupported');
      assert.equal(codec.streaming, true, codec.type + ' streaming unsupported');
    }
    assert.equal(result.video.width, 64);
    assert.ok(result.video.frames >= 10, 'video frames were not decoded');
    assert.ok(result.video.time >= 1, 'video did not play to completion');
    assert.ok(result.audio.time >= 1, 'audio did not play to completion');
    console.log(JSON.stringify({browser: browser.version(), arch: process.arch, ...result}));
  } finally {
    await browser.close();
  }
});
