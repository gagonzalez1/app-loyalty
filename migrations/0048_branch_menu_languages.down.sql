DO $$
BEGIN
 IF EXISTS (SELECT 1 FROM branch_menu_documents WHERE language <> 'es')
    OR EXISTS (SELECT 1 FROM branch_menus WHERE language <> 'es') THEN
  RAISE EXCEPTION 'cannot remove language support while English menu records exist';
 END IF;
END $$;

ALTER TABLE branch_menus
 DROP CONSTRAINT branch_menu_draft_language_fk,
 DROP CONSTRAINT branch_menu_published_language_fk,
 DROP CONSTRAINT branch_menus_pkey,
 DROP COLUMN language,
 ADD PRIMARY KEY (sucursal_id);

ALTER TABLE branch_menu_documents
 DROP CONSTRAINT branch_menu_document_language_key,
 DROP COLUMN language;
