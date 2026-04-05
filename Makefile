PREFIX ?= /usr/local

install:
	install -d $(DESTDIR)$(PREFIX)/bin
	install -m 755 ccswitch.sh $(DESTDIR)$(PREFIX)/bin/ccswitch

uninstall:
	rm -f $(DESTDIR)$(PREFIX)/bin/ccswitch

.PHONY: install uninstall
