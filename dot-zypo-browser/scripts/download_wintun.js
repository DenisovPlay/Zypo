const https = require('https');
const fs = require('fs');
const path = require('path');
const { execSync } = require('child_process');

const WINTUN_URL = 'https://www.wintun.net/builds/wintun-0.14.1.zip';
const ZIP_PATH = path.join(__dirname, '../bin/wintun.zip');
const BIN_DIR = path.join(__dirname, '../bin');

if (!fs.existsSync(BIN_DIR)) {
  fs.mkdirSync(BIN_DIR, { recursive: true });
}

if (fs.existsSync(path.join(BIN_DIR, 'wintun-amd64.dll')) && fs.existsSync(path.join(BIN_DIR, 'wintun-arm64.dll'))) {
  console.log('Wintun DLLs already downloaded.');
  process.exit(0);
}

console.log('Downloading Wintun...');
const file = fs.createWriteStream(ZIP_PATH);
https.get(WINTUN_URL, (response) => {
  response.pipe(file);
  file.on('finish', () => {
    file.close();
    console.log('Extracting Wintun...');
    try {
      execSync(`unzip -j -o "${ZIP_PATH}" "wintun/bin/amd64/wintun.dll" -d "${BIN_DIR}"`);
      fs.renameSync(path.join(BIN_DIR, 'wintun.dll'), path.join(BIN_DIR, 'wintun-amd64.dll'));

      execSync(`unzip -j -o "${ZIP_PATH}" "wintun/bin/arm64/wintun.dll" -d "${BIN_DIR}"`);
      fs.renameSync(path.join(BIN_DIR, 'wintun.dll'), path.join(BIN_DIR, 'wintun-arm64.dll'));
      
      fs.unlinkSync(ZIP_PATH);
      console.log('Wintun downloaded and extracted successfully.');
    } catch (e) {
      console.error('Error extracting Wintun:', e.message);
    }
  });
}).on('error', (err) => {
  fs.unlink(ZIP_PATH, () => {});
  console.error('Error downloading Wintun:', err.message);
});
