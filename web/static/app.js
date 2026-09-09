// Live updates, and nothing else: the page is server-rendered and the forms
// are the way to act. Loaded only while something runs. The server sends
// the pieces that changed, each a root element carrying its id, rendered by
// the same templates as the page; they replace what is there. "end" says
// nothing runs any more.
(function () {
  var es = new EventSource("/events");
  es.addEventListener("fragment", function (e) {
    var t = document.createElement("template");
    t.innerHTML = e.data;
    var next = t.content.firstElementChild;
    var cur = next && document.getElementById(next.id);
    if (cur) cur.replaceWith(next);
  });
  es.addEventListener("end", function () { es.close(); });
})();
