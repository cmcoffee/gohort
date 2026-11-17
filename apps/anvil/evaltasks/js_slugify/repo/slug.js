'use strict';
// slugify turns a title into a URL path segment: lower case, words joined by single dashes.
function slugify(title) {
  return title.toLowerCase().replace(/[^a-z0-9]/g, '-');
}
module.exports = { slugify: slugify };
