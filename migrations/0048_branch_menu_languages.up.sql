-- deploy: expand
ALTER TABLE branch_menu_documents
 ADD COLUMN language TEXT NOT NULL DEFAULT 'es' CHECK (language IN ('es', 'en')),
 ADD CONSTRAINT branch_menu_document_language_key UNIQUE (id, sucursal_id, marca_id, language);

ALTER TABLE branch_menus
 ADD COLUMN language TEXT NOT NULL DEFAULT 'es' CHECK (language IN ('es', 'en')),
 DROP CONSTRAINT branch_menus_pkey,
 ADD PRIMARY KEY (sucursal_id, language),
 ADD CONSTRAINT branch_menu_draft_language_fk FOREIGN KEY (draft_document_id, sucursal_id, marca_id, language)
  REFERENCES branch_menu_documents(id, sucursal_id, marca_id, language),
 ADD CONSTRAINT branch_menu_published_language_fk FOREIGN KEY (published_document_id, sucursal_id, marca_id, language)
  REFERENCES branch_menu_documents(id, sucursal_id, marca_id, language);
