// Live updates, and nothing else: the page is server-rendered and the forms
// are the way to act. Included only while something runs; the server sends
// one "change" when it is over and the page reloads — a GET of /, never a
// re-POST.
(function () {
  var es = new EventSource("/events");
  es.addEventListener("change", function () {
    es.close();
    location.replace("/");
  });
})();
