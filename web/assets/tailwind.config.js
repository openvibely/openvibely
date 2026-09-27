// Used by scripts/build-assets.sh with Tailwind's standalone binary (no Node needed).
// Tailwind only keeps classes it finds in these files, so a class name must appear written
// out in full somewhere below (not assembled from pieces at runtime) or it will be missing
// from the stylesheets.
module.exports = {
  content: [
    '../templates/**/*.templ',
    '../**/*.go',
    '../../internal/**/*.go',
    '!../**/*_test.go',
    '!../../internal/**/*_test.go',
  ],
};
