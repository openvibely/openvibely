// Collect notices from the dependencies actually installed in this image.
const fs = require('node:fs');
const path = require('node:path');
const {execFileSync} = require('node:child_process');
const {chromium} = require('/opt/browser/node_modules/playwright');
const root = '/usr/share/licenses/openvibely-computer';

async function main() {
  fs.mkdirSync(root, {recursive: true});
  const inventory = execFileSync('dpkg-query', ['-W', '-f=${db:Status-Status}\t${binary:Package}\t${Version}\t${source:Package}\t${source:Version}\n'], {encoding: 'utf8'})
    .split('\n').filter(line => line.startsWith('installed\t')).map(line => line.slice(10)).join('\n') + '\n';
  fs.writeFileSync(path.join(root, 'ubuntu-packages.tsv'), inventory);
  const sources = new Map();
  for (const line of inventory.trim().split('\n')) {
    const [, , name, version] = line.split('\t');
    if (!name || !version) throw new Error(`Missing Ubuntu source identity: ${line}`);
    sources.set(`${name}\t${version}`, `https://launchpad.net/ubuntu/+source/${encodeURIComponent(name)}/${encodeURIComponent(version)}`);
  }
  fs.writeFileSync(`${root}/ubuntu-sources.tsv`, 'source_package\tsource_version\tsource_url\n' +
    [...sources].sort(([a], [b]) => a.localeCompare(b)).map(([identity, url]) => `${identity}\t${url}`).join('\n') + '\n');
  fs.writeFileSync(`${root}/SOURCES.txt`, `Ubuntu dependency source downloads

Ubuntu publishes the source for the Ubuntu packages in this image:
https://launchpad.net/ubuntu/+source

See ubuntu-sources.tsv in this directory for a link to each exact source
package version installed in this image. Open its link and use the source
package downloads, including the .dsc file, original source archive(s), and
Debian/Ubuntu patch or packaging archive. These include Ubuntu's changes;
the upstream project's latest source alone may not match the installed binary.
ubuntu-packages.tsv maps installed binary packages to those source versions.

These links cover packages installed through Ubuntu, not the separately
installed Node.js, npm, Playwright, browser, or Go dependencies. Their notices and version
information are also recorded in this directory.

If matching source cannot be obtained, contact admin@openvibely.ai and identify
the image and package version. See SOURCE-OFFER.txt for our source offer.
`);
  for (const line of inventory.trim().split('\n')) {
    const name = line.split('\t')[0].split(':')[0];
    const copyright = `/usr/share/doc/${name}/copyright`;
    if (!fs.existsSync(copyright)) throw new Error(`Missing package copyright: ${name}`);
    fs.mkdirSync(`${root}/ubuntu/${name}`, {recursive: true});
    fs.copyFileSync(copyright, `${root}/ubuntu/${name}/copyright`);
  }
  fs.cpSync('/usr/share/common-licenses', `${root}/common-licenses`, {recursive: true, dereference: true});
  function collect(directory, relative = '') {
    for (const entry of fs.readdirSync(directory, {withFileTypes: true})) {
      const source = path.join(directory, entry.name);
      const target = path.join(root, 'npm', relative, entry.name);
      if (entry.isDirectory()) collect(source, path.join(relative, entry.name));
      else if (/^(licen[cs]e|copying|notice|thirdpartynotices)(\.|$)/i.test(entry.name) || entry.name === 'package.json') {
        fs.mkdirSync(path.dirname(target), {recursive: true});
        fs.copyFileSync(source, target);
      }
    }
  }
  collect('/opt/browser/node_modules');
  collect('/usr/local/lib/node_modules', 'node-runtime');
  fs.writeFileSync(`${root}/node/version.txt`, process.version + '\n');
  const browser = await chromium.launch({executablePath: chromium.executablePath(), args: ['--no-sandbox']});
  try {
    const page = await browser.newPage();
    await page.goto('chrome://credits/');
    const credits = await page.locator('body').textContent();
    if (!credits.includes('Copyright') || credits.length < 10000) throw new Error('Chromium credits are missing or incomplete');
    fs.writeFileSync(`${root}/chromium-credits.txt`, credits);
    fs.writeFileSync(`${root}/chromium-version.txt`, browser.version() + '\n');
    await page.goto('chrome://version/');
    fs.writeFileSync(`${root}/chromium-build.txt`, await page.locator('body').textContent());
  } finally {
    await browser.close();
  }

}
main().catch(error => {console.error(error); process.exitCode = 1;});
