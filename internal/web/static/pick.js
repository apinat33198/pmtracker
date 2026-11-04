// Search box for pickers. A <select data-search="system"> becomes a text box
// that filters its options by name and other names (data-aliases) as you
// type. Only listed choices can be picked; when the select has an "add new"
// option (value __new__) and nothing matches exactly, "Add “text” as a new
// system" is offered as a separate choice. The select stays in the form and
// carries the value, so the server sees exactly what the plain dropdown
// sends; without this script the dropdown works as it is.
(function () {
  "use strict";
  var NEW = "__new__", seq = 0;

  function norm(s) { return (s || "").toLowerCase().replace(/\s+/g, " ").trim(); }
  function tidy(s) { return (s || "").replace(/\s+/g, " ").trim(); }

  function enhance(sel) {
    var noun = sel.getAttribute("data-search") || "item";
    var box = document.getElementById(sel.getAttribute("data-new-box") || "");
    var newInput = box ? box.querySelector("input") : null;
    var items = [], canAdd = false;
    Array.prototype.forEach.call(sel.options, function (o) {
      if (o.value === NEW) { canAdd = true; return; }
      if (o.value === "") return;
      var aliases = (o.getAttribute("data-aliases") || "").split(",").map(tidy).filter(Boolean);
      items.push({ value: o.value, label: o.text, aliases: aliases });
    });

    var id = "pick" + (++seq);
    var wrap = document.createElement("span");
    wrap.className = "combo";
    var input = document.createElement("input");
    input.type = "text";
    input.autocomplete = "off";
    input.spellcheck = false;
    input.placeholder = "Type to search";
    input.setAttribute("role", "combobox");
    input.setAttribute("aria-autocomplete", "list");
    input.setAttribute("aria-expanded", "false");
    input.setAttribute("aria-controls", id + "-list");
    input.setAttribute("aria-label", sel.getAttribute("aria-label") || noun);
    var list = document.createElement("ul");
    list.id = id + "-list";
    list.className = "combo-list";
    list.setAttribute("role", "listbox");
    list.hidden = true;
    var hint = document.createElement("span");
    hint.className = "field-hint combo-new";
    hint.hidden = true;
    wrap.appendChild(input);
    wrap.appendChild(list);
    sel.parentNode.insertBefore(wrap, sel);
    sel.parentNode.insertBefore(hint, sel);
    // The select keeps the value but is no longer shown, and the box takes
    // over "required" (a hidden required field would block the form silently).
    input.required = sel.required;
    sel.required = false;
    sel.hidden = true;
    sel.tabIndex = -1;
    sel.removeAttribute("data-new-box");
    if (box) box.hidden = true;

    var current = null, shown = [], active = -1;
    items.forEach(function (it) { if (it.value === sel.value) current = it; });
    input.value = current ? current.label : "";

    // exact finds the item whose name or other name is the text.
    function exact(text) {
      var q = norm(text);
      if (!q) return null;
      for (var i = 0; i < items.length; i++) {
        if (norm(items[i].label) === q) return items[i];
      }
      for (i = 0; i < items.length; i++) {
        for (var j = 0; j < items[i].aliases.length; j++) {
          if (norm(items[i].aliases[j]) === q) return items[i];
        }
      }
      return null;
    }

    // matches lists items for the text: names starting with it, then names
    // containing it, then other names containing it.
    function matches(text) {
      var q = norm(text), starts = [], within = [], other = [];
      items.forEach(function (it) {
        var n = norm(it.label);
        if (!q || n.indexOf(q) === 0) { starts.push({ it: it }); return; }
        if (n.indexOf(q) >= 0) { within.push({ it: it }); return; }
        for (var j = 0; j < it.aliases.length; j++) {
          if (norm(it.aliases[j]).indexOf(q) >= 0) { other.push({ it: it, alias: it.aliases[j] }); return; }
        }
      });
      return starts.concat(within, other);
    }

    function validate() {
      var ok;
      if (!tidy(input.value)) ok = !input.required;
      else if (current && current.value === NEW) ok = norm(input.value) === norm(current.label);
      else ok = !!current && exact(input.value) === current;
      input.setCustomValidity(ok ? "" : "Pick a " + noun + " from the list" + (canAdd ? ", or add it as a new " + noun : "") + ".");
      var isNew = current && current.value === NEW && ok;
      hint.hidden = !isNew;
      if (isNew) hint.textContent = "“" + current.label + "” will be added as a new " + noun + " when you save.";
    }

    function choose(m) {
      if (m.add) {
        current = { value: NEW, label: m.add };
        sel.value = NEW;
        if (newInput) newInput.value = m.add;
      } else {
        current = m.it;
        sel.value = m.it.value;
        if (newInput) newInput.value = "";
      }
      input.value = current.label;
      validate();
    }

    function highlight() {
      Array.prototype.forEach.call(list.children, function (li, i) {
        li.classList.toggle("active", i === active);
        if (i === active && li.scrollIntoView) li.scrollIntoView({ block: "nearest" });
      });
      if (active >= 0) input.setAttribute("aria-activedescendant", id + "-" + active);
      else input.removeAttribute("aria-activedescendant");
    }

    function close() {
      list.hidden = true;
      input.setAttribute("aria-expanded", "false");
      input.removeAttribute("aria-activedescendant");
    }

    function render() {
      var text = input.value;
      // Showing the chosen one's name: list everything, like a dropdown.
      var all = current && current.value !== NEW && text === current.label;
      shown = matches(all ? "" : text);
      if (canAdd && tidy(text).length >= 2 && !all && !exact(text)) shown.push({ add: tidy(text) });
      list.textContent = "";
      active = -1;
      shown.forEach(function (m, i) {
        var li = document.createElement("li");
        li.id = id + "-" + i;
        li.setAttribute("role", "option");
        if (m.add) {
          li.className = "combo-add";
          li.textContent = "Add “" + m.add + "” as a new " + noun;
        } else {
          li.textContent = m.it.label;
          if (m.alias) {
            var s = document.createElement("span");
            s.className = "combo-alias";
            s.textContent = m.alias;
            li.appendChild(s);
          }
          if (current === m.it) {
            li.setAttribute("aria-selected", "true");
            if (all) active = i;
          }
        }
        li.addEventListener("mousedown", function (e) { e.preventDefault(); });
        li.addEventListener("click", function () { choose(m); close(); });
        list.appendChild(li);
      });
      if (!shown.length) {
        var none = document.createElement("li");
        none.className = "combo-none";
        none.textContent = "No " + noun + " matches";
        list.appendChild(none);
      }
      if (active < 0 && shown.length && !all) active = 0;
      list.hidden = false;
      input.setAttribute("aria-expanded", "true");
      highlight();
    }

    input.addEventListener("focus", function () { input.select(); render(); });
    input.addEventListener("click", function () { if (list.hidden) render(); });
    input.addEventListener("input", function () {
      var m = exact(input.value);
      if (m && m !== current) {
        current = m;
        sel.value = m.value;
        if (newInput) newInput.value = "";
      }
      validate();
      render();
    });
    input.addEventListener("keydown", function (e) {
      if (e.key === "ArrowDown" || e.key === "ArrowUp") {
        e.preventDefault();
        if (list.hidden) { render(); return; }
        if (!shown.length) return;
        active = (active + (e.key === "ArrowDown" ? 1 : -1) + shown.length) % shown.length;
        highlight();
      } else if (e.key === "Enter") {
        if (!list.hidden && shown[active]) {
          e.preventDefault();
          choose(shown[active]);
          close();
        }
      } else if (e.key === "Escape") {
        if (!list.hidden) {
          e.preventDefault();
          close();
          if (current) input.value = current.label;
          validate();
        }
      } else if (e.key === "Tab") {
        // Tab takes the highlighted match, never "add new".
        if (!list.hidden && shown[active] && !shown[active].add && exact(input.value) !== current) choose(shown[active]);
        close();
      }
    });
    input.addEventListener("blur", function () {
      // Late, so a tap on the list lands first on phones.
      setTimeout(function () {
        close();
        var m = exact(input.value);
        if (m) choose({ it: m });
        else validate();
      }, 150);
    });
    validate();
  }

  document.querySelectorAll("select[data-search]").forEach(enhance);
})();
